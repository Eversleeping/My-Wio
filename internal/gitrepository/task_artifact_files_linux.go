//go:build linux

package gitrepository

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
	"golang.org/x/sys/unix"
)

// Open each path component relative to its pinned parent. A concurrent
// symlink or special-file replacement cannot redirect or block the read.
func artifactWorkingFile(root, name string) (artifactContent, error) {
	dir, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return artifactContent{}, errors.New("could not open artifact workspace")
	}
	defer func() { _ = unix.Close(dir) }()
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		child, err := unix.Openat(dir, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			return artifactContent{}, nil
		}
		if err != nil {
			return artifactContent{}, errors.New("artifact parent must be a regular directory")
		}
		_ = unix.Close(dir)
		dir = child
	}
	fd, err := unix.Openat(dir, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return artifactContent{}, nil
	}
	if err != nil {
		return artifactContent{}, errors.New("could not open artifact file")
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > protocol.MaxArtifactBytes {
		return artifactContent{}, errors.New("artifact path must be a bounded regular file")
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
