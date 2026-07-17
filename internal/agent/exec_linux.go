//go:build linux

package agent

import (
	"os"
	"syscall"
)

func execAgentBinary(path string) error {
	arguments := append([]string{path}, os.Args[1:]...)
	return syscall.Exec(path, arguments, os.Environ())
}
