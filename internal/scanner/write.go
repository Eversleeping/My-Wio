package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

// WriteWorkspaceFile only writes regular files inside configured workspace roots.
// Creates never overwrite; edits require the full original content hash.
func WriteWorkspaceFile(ctx context.Context, command protocol.WorkspaceFileWriteCommand, allowedRoots []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(command.Content) > protocol.MaxWorkspaceFileBytes {
		return errors.New("file exceeds the 1 MiB limit")
	}
	root, err := resolveWorkspaceRoot(command.Root, allowedRoots)
	if err != nil {
		return err
	}
	relative := strings.ReplaceAll(command.Path, "\\", "/")
	if relative == "" || strings.HasPrefix(relative, "/") || strings.ContainsAny(relative, ":\x00") {
		return errors.New("invalid workspace file path")
	}
	parts := strings.Split(relative, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return errors.New("invalid workspace file path")
		}
	}
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) && command.Create {
			if err = os.Mkdir(parent, 0755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(parent)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("file parent must be a directory, not a symbolic link")
		}
	}
	target := filepath.Join(parent, parts[len(parts)-1])
	mode := os.FileMode(0644)
	checkOriginal := func() error {
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("only regular files can be edited")
		}
		if info.Size() > protocol.MaxWorkspaceFileBytes {
			return errors.New("file exceeds the 1 MiB limit")
		}
		file, err := os.Open(target)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, io.LimitReader(file, protocol.MaxWorkspaceFileBytes+1))
		file.Close()
		if readErr != nil {
			return readErr
		}
		if command.ExpectedHash == "" || hex.EncodeToString(hash.Sum(nil)) != command.ExpectedHash {
			return errors.New("file changed since preview; refresh it before editing again")
		}
		mode = info.Mode().Perm()
		return nil
	}
	if !command.Create {
		if err = checkOriginal(); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".wio-write-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(command.Content); err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if command.Create {
		// Linking the complete temporary file provides atomic no-clobber creation.
		if err = os.Link(temporary, target); errors.Is(err, os.ErrExist) {
			return errors.New("file already exists; choose another path")
		}
		return err
	}
	if err = checkOriginal(); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}
