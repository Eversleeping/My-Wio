//go:build !linux

package gitrepository

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

func artifactWorkingFile(root, name string) (artifactContent, error) {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			return artifactContent{}, nil
		}
		if err != nil {
			return artifactContent{}, err
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return artifactContent{}, errors.New("artifact parent must be a regular directory")
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > protocol.MaxArtifactBytes {
			return artifactContent{}, errors.New("artifact path must be a bounded regular file")
		}
		file, err := os.Open(root)
		if err != nil {
			return artifactContent{}, err
		}
		defer file.Close()
		actual, err := file.Stat()
		if err != nil || !os.SameFile(info, actual) {
			return artifactContent{}, errors.New("artifact file changed while opening")
		}
		data, err := io.ReadAll(io.LimitReader(file, protocol.MaxArtifactBytes+1))
		if err != nil || int64(len(data)) != info.Size() {
			return artifactContent{}, errors.New("artifact file changed while reading")
		}
		mode := "100644"
		if info.Mode().Perm()&0100 != 0 {
			mode = "100755"
		}
		return artifactContent{mode, data}, nil
	}
	return artifactContent{}, errors.New("invalid artifact path")
}
