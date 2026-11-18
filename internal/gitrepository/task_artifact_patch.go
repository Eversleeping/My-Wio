package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
)

// Build a Git binary patch from exact file bytes in an isolated scratch
// object database. No user refs, index, attributes, or filters participate.
func taskArtifactPatch(ctx context.Context, old []artifactContent, files []protocol.ArtifactFile, shaLength int) (string, func(), error) {
	root, err := os.MkdirTemp("", "wio-task-patch-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()
	run := func(input []byte, args ...string) ([]byte, error) {
		cmd := childprocess.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(input)
		cmd.Env = childprocess.Environment(childprocess.WithScope(ctx, childprocess.Git),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		return childprocess.Output(cmd)
	}
	format := "sha1"
	if shaLength == 64 {
		format = "sha256"
	}
	if _, err := run(nil, "init", "--quiet", "--template=", "--object-format="+format); err != nil {
		return "", nil, errors.New("could not prepare scratch Git index")
	}
	writeIndex := func(before bool) error {
		var input bytes.Buffer
		for i, file := range files {
			content := artifactContent{file.AfterMode, file.Content}
			if before {
				content = old[i]
			}
			if content.mode == "" {
				fmt.Fprintf(&input, "0 %s\t%s%c", strings.Repeat("0", shaLength), file.Path, 0)
				continue
			}
			object, err := run(content.data, "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
			fmt.Fprintf(&input, "%s %s\t%s%c", content.mode, strings.TrimSpace(string(object)), file.Path, 0)
		}
		_, err := run(input.Bytes(), "update-index", "-z", "--index-info")
		return err
	}
	if err := writeIndex(true); err != nil {
		return "", nil, err
	}
	tree, err := run(nil, "write-tree")
	if err != nil {
		return "", nil, err
	}
	if err := writeIndex(false); err != nil {
		return "", nil, err
	}
	raw, err := run(nil, "diff", "--cached", "--binary", "--no-renames", "--no-ext-diff", "--no-textconv", strings.TrimSpace(string(tree)))
	if err != nil || len(raw) == 0 {
		return "", nil, errors.New("could not generate artifact patch")
	}
	patch := filepath.Join(root, "patch")
	if err := os.WriteFile(patch, raw, 0600); err != nil {
		return "", nil, err
	}
	success = true
	return patch, cleanup, nil
}
