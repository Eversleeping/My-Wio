//go:build !windows

package hostplatform

import "os"

func ReplaceFile(source, target string) error { return os.Rename(source, target) }
