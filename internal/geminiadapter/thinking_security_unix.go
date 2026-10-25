//go:build !windows

package geminiadapter

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func validateThinkingDirectory(directory string) error {
	// Match the official CLI's ownership and ancestor permission checks.
	if os.Geteuid() != 0 {
		return errors.New("Gemini system overrides require administrator ownership")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	for _, start := range []string{absolute, resolved} {
		for current := start; ; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 || (info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 != 0) {
				return errors.New("insecure Gemini system settings directory")
			}
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	return nil
}
