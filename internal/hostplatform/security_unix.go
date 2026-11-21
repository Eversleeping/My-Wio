//go:build !windows

package hostplatform

import (
	"os"
)

func ProtectInstaller(path string) error                           { return os.Chmod(path, 0700) }
func Private(info os.FileInfo, path string, mode os.FileMode) bool { return info.Mode().Perm() == mode }
func Administrator() bool                                          { return os.Geteuid() == 0 }

func nativeLayout(layout Layout) Layout      { return layout }
func ProtectUserDirectory(path string) error { return os.Chmod(path, 0700) }
