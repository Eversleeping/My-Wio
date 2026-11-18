package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/taskartifact"
)

// ErrArtifactHandoffUnknown indicates that a failed apply could not be
// verified as restored. The control plane must reserve the worktree.
var ErrArtifactHandoffUnknown = errors.New("artifact handoff could not be safely restored")

type artifactContent struct {
	mode string
	data []byte
}

func (f artifactContent) hash() string {
	if f.mode == "" {
		return ""
	}
	return taskartifact.Hash(f.data)
}

func artifactBaseline(ctx context.Context, root string) (string, error) {
	top, err := InspectOutput(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSuffix(string(top), "\n")) != root {
		return "", errors.New("artifact workspace must be a Git worktree root")
	}
	head, err := InspectOutput(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", errors.New("artifact workspace needs a Git commit")
	}
	return strings.TrimSpace(string(head)), nil
}

func artifactBaseFile(ctx context.Context, root, base, name string) (artifactContent, error) {
	raw, err := InspectOutput(ctx, root, "ls-tree", "-z", base, "--", ":(literal)"+name)
	if err != nil || len(raw) == 0 {
		return artifactContent{}, err
	}
	header, entry, _ := strings.Cut(string(raw), "\t")
	fields := strings.Fields(header)
	if len(fields) != 3 || entry != name+"\x00" || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return artifactContent{}, errors.New("artifact baseline entry is not a regular file")
	}
	size, err := InspectOutput(ctx, root, "cat-file", "-s", fields[2])
	n, parseErr := strconv.Atoi(strings.TrimSpace(string(size)))
	if err != nil || parseErr != nil || n < 0 || n > protocol.MaxArtifactBytes {
		return artifactContent{}, errors.New("artifact baseline file exceeds the size limit")
	}
	data, err := InspectOutput(ctx, root, "cat-file", "blob", fields[2])
	if err != nil || len(data) != n {
		return artifactContent{}, errors.New("could not read artifact baseline")
	}
	return artifactContent{fields[0], data}, nil
}

func CaptureTaskArtifact(ctx context.Context, root string, paths []string) (protocol.ArtifactManifest, error) {
	var manifest protocol.ArtifactManifest
	if err := taskartifact.Paths(paths); err != nil {
		return manifest, err
	}
	base, err := artifactBaseline(ctx, root)
	if err != nil {
		return manifest, err
	}
	manifest.BaseSHA = base
	size, baselineSize := 0, 0
	for _, name := range paths {
		before, err := artifactBaseFile(ctx, root, base, name)
		if err != nil {
			return manifest, err
		}
		if before.mode == "" {
			ignored, err := InspectOutput(ctx, root, "check-ignore", "--", name)
			var exit *exec.ExitError
			if len(ignored) != 0 || err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
				return manifest, errors.New("artifact cannot capture an ignored file")
			}
		}
		after, err := artifactWorkingFile(root, name, false)
		if err != nil {
			return manifest, err
		}
		size += len(after.data)
		baselineSize += len(before.data)
		if size > protocol.MaxArtifactBytes || baselineSize > protocol.MaxArtifactBytes {
			return manifest, errors.New("artifact exceeds the size limit")
		}
		manifest.Files = append(manifest.Files, protocol.ArtifactFile{
			Path: name, BeforeMode: before.mode, BeforeHash: before.hash(), AfterMode: after.mode, AfterHash: after.hash(), Content: after.data,
		})
	}
	// Changes during capture require a new user-selected snapshot.
	head, err := artifactBaseline(ctx, root)
	if err != nil || head != base {
		return manifest, errors.New("Git baseline changed while capturing artifact")
	}
	for _, selected := range manifest.Files {
		current, err := artifactWorkingFile(root, selected.Path, false)
		if err != nil || current.hash() != selected.AfterHash || current.mode != selected.AfterMode {
			return manifest, errors.New("a selected file changed while capturing artifact")
		}
	}
	_, err = taskartifact.Validate(manifest)
	return manifest, err
}

func taskArtifactStatus(ctx context.Context, root string) ([]string, error) {
	raw, err := InspectOutput(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=none")
	if err != nil {
		return nil, errors.New("could not inspect target workspace")
	}
	var paths []string
	for _, entry := range bytes.Split(raw, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 4 || (entry[0] != ' ' && !bytes.HasPrefix(entry, []byte("?? "))) {
			return nil, errors.New("target workspace contains staged or unresolved changes")
		}
		paths = append(paths, string(entry[3:]))
	}
	return paths, nil
}

// HandoffTaskArtifact checks every baseline and path before invoking Git's
// whole-patch validation. Git never stages, commits, or applies a partial
// patch through --reject/--3way. A replay verifies the exact resulting files.
func HandoffTaskArtifact(ctx context.Context, root string, command protocol.TaskArtifactHandoff) error {
	if command.Action != "apply" && command.Action != "preflight" && command.Action != "verify" {
		return errors.New("invalid artifact handoff action")
	}
	if command.MetadataOnly {
		if command.Action != "verify" || command.Previous != nil {
			return errors.New("metadata-only artifacts can only verify a prepared worktree")
		}
		manifest, err := readPreparedArtifact(root, command.Manifest)
		if err != nil {
			return err
		}
		command.Manifest = manifest
	}
	digest, err := taskartifact.Validate(command.Manifest)
	if err != nil || digest != command.Digest {
		return errors.New("artifact digest does not match")
	}
	base, err := artifactBaseline(ctx, root)
	if err != nil || base != command.Manifest.BaseSHA {
		return errors.New("target Git baseline differs from the artifact")
	}
	dirty, err := taskArtifactStatus(ctx, root)
	if err != nil {
		return err
	}
	// A clean target is at its Git baseline, even if the control plane has
	// evidence of an older handoff which was subsequently cleaned.
	if len(dirty) == 0 {
		command.Previous = nil
	}
	files, old, err := artifactTransition(ctx, root, command)
	if err != nil {
		return err
	}
	before, replay := true, true
	allowedBefore, allowedAfter := map[string]bool{}, map[string]bool{}
	if command.Previous != nil {
		for _, file := range command.Previous.Files {
			allowedBefore[file.Path] = true
		}
	}
	for _, file := range command.Manifest.Files {
		allowedAfter[file.Path] = true
	}
	for _, name := range dirty {
		before = before && allowedBefore[name]
		replay = replay && allowedAfter[name]
	}
	for i, file := range files {
		current, err := artifactWorkingFile(root, file.Path, artifactPathOverlap(files, file.Path))
		if err != nil {
			return err
		}
		before = before && sameArtifactContent(current, old[i])
		replay = replay && current.mode == file.AfterMode && current.hash() == file.AfterHash
	}
	if replay && command.Action != "preflight" {
		return nil
	}
	if command.Action == "verify" || !before {
		return errors.New("target workspace no longer matches the artifact baseline or prepared contents")
	}
	changed := files[:0]
	prior := old[:0]
	for i, file := range files {
		if old[i].mode != file.AfterMode || old[i].hash() != file.AfterHash {
			changed = append(changed, file)
			prior = append(prior, old[i])
		}
	}
	files, old = changed, prior
	if len(files) == 0 {
		return nil
	}
	patch, cleanup, err := taskArtifactPatch(ctx, old, files, len(base))
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{"apply", "--whitespace=nowarn", "--check", "--", patch}
	if _, err := RunInspect(ctx, root, args...); err != nil {
		return errors.New("artifact conflicts with the target workspace")
	}
	if command.Action == "preflight" {
		return nil
	}
	head, err := artifactBaseline(ctx, root)
	if err != nil || head != base {
		return errors.New("target Git baseline changed before applying")
	}
	_, applyErr := RunInspect(ctx, root, "apply", "--whitespace=nowarn", "--", patch)
	if applyErr == nil {
		applyErr = verifyArtifactTransition(root, files)
		if applyErr == nil {
			return nil
		}
	}
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := restoreArtifactTransition(restoreCtx, root, base, files, old); err != nil {
		return ErrArtifactHandoffUnknown
	}
	return errors.New("Git could not apply and verify the artifact; the prior contents were restored")
}

// Reconstruct the original manifest from the worktree. The digest
// still covers the contents, and paths are checked before opening any file.
func readPreparedArtifact(root string, manifest protocol.ArtifactManifest) (protocol.ArtifactManifest, error) {
	paths := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		paths = append(paths, file.Path)
	}
	if err := taskartifact.Paths(paths); err != nil {
		return manifest, err
	}
	manifest.Files = append([]protocol.ArtifactFile(nil), manifest.Files...)
	size := 0
	for i, file := range manifest.Files {
		current, err := artifactWorkingFile(root, file.Path, false)
		if err != nil || current.mode != file.AfterMode || current.hash() != file.AfterHash {
			return manifest, errors.New("target workspace no longer matches the prepared artifact")
		}
		size += len(current.data)
		if size > protocol.MaxArtifactBytes {
			return manifest, errors.New("prepared artifact exceeds the size limit")
		}
		manifest.Files[i].Content = current.data
	}
	return manifest, nil
}

func sameArtifactContent(left, right artifactContent) bool {
	return left.mode == right.mode && left.hash() == right.hash()
}

func artifactPathOverlap(files []protocol.ArtifactFile, path string) bool {
	for _, file := range files {
		if strings.HasPrefix(file.Path, path+"/") || strings.HasPrefix(path, file.Path+"/") {
			return true
		}
	}
	return false
}

func verifyArtifactTransition(root string, files []protocol.ArtifactFile) error {
	for _, file := range files {
		actual, err := artifactWorkingFile(root, file.Path, artifactPathOverlap(files, file.Path))
		if err != nil || actual.mode != file.AfterMode || actual.hash() != file.AfterHash {
			return errors.New("applied artifact does not match its manifest")
		}
	}
	return nil
}

func restoreArtifactTransition(ctx context.Context, root, base string, files []protocol.ArtifactFile, old []artifactContent) error {
	head, err := artifactBaseline(ctx, root)
	if err != nil || head != base {
		return ErrArtifactHandoffUnknown
	}
	var changed []protocol.ArtifactFile
	var current []artifactContent
	restore := make([]protocol.ArtifactFile, 0, len(files))
	for i, file := range files {
		prior := protocol.ArtifactFile{Path: file.Path, AfterMode: old[i].mode, AfterHash: old[i].hash(), Content: old[i].data}
		restore = append(restore, prior)
		actual, err := artifactWorkingFile(root, file.Path, artifactPathOverlap(files, file.Path))
		if err != nil {
			return err
		}
		if sameArtifactContent(actual, old[i]) {
			continue
		}
		if actual.mode != file.AfterMode || actual.hash() != file.AfterHash {
			return ErrArtifactHandoffUnknown
		}
		changed = append(changed, prior)
		current = append(current, actual)
	}
	if len(changed) > 0 {
		patch, cleanup, err := taskArtifactPatch(ctx, current, changed, len(base))
		if err != nil {
			return err
		}
		defer cleanup()
		if _, err := RunInspect(ctx, root, "apply", "--whitespace=nowarn", "--", patch); err != nil {
			return err
		}
	}
	return verifyArtifactTransition(root, restore)
}

// artifactTransition builds one patch from the previously prepared snapshot
// to the new one. Paths omitted in the new snapshot return to the baseline.
func artifactTransition(ctx context.Context, root string, command protocol.TaskArtifactHandoff) ([]protocol.ArtifactFile, []artifactContent, error) {
	previous := map[string]protocol.ArtifactFile{}
	paths := []string{}
	byPath := map[string]protocol.ArtifactFile{}
	for _, file := range command.Manifest.Files {
		paths = append(paths, file.Path)
		byPath[file.Path] = file
	}
	if command.Previous != nil {
		if _, err := taskartifact.Validate(*command.Previous); err != nil || command.Previous.BaseSHA != command.Manifest.BaseSHA {
			return nil, nil, errors.New("previous and new artifact Git baselines differ")
		}
		for _, file := range command.Previous.Files {
			previous[file.Path] = file
			if _, ok := byPath[file.Path]; !ok {
				paths = append(paths, file.Path)
			}
		}
	}
	var files []protocol.ArtifactFile
	var old []artifactContent
	previousSize, size := 0, 0
	for _, name := range paths {
		base, err := artifactBaseFile(ctx, root, command.Manifest.BaseSHA, name)
		if err != nil {
			return nil, nil, err
		}
		current := base
		if file, ok := previous[name]; ok {
			previousSize += len(base.data)
			if file.BeforeHash != base.hash() || file.BeforeMode != base.mode {
				return nil, nil, errors.New("previous artifact baseline does not match the target")
			}
			current = artifactContent{file.AfterMode, file.Content}
		}
		file, ok := byPath[name]
		if !ok {
			file = protocol.ArtifactFile{Path: name, AfterMode: base.mode, AfterHash: base.hash(), Content: base.data}
		} else {
			size += len(base.data)
			if file.BeforeHash != base.hash() || file.BeforeMode != base.mode {
				return nil, nil, errors.New("target Git file baseline differs from the artifact")
			}
		}
		if size > protocol.MaxArtifactBytes || previousSize > protocol.MaxArtifactBytes {
			return nil, nil, errors.New("artifact baseline exceeds the size limit")
		}
		old = append(old, current)
		files = append(files, file)
	}
	return files, old, nil
}
