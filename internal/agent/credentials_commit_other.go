//go:build !windows

package agent

import (
	"os"
	"path/filepath"
)

func commitCredentialFile(temporary, path string) error {
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
