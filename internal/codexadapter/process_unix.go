//go:build !windows

package codexadapter

import (
	"os/exec"
	"syscall"

	"github.com/wio-platform/wio/internal/childprocess"
)

// configureProcess places the app-server in its own process group so stopping
// a session also stops commands and tools it started, instead of leaving them
// running under init.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = childprocess.WaitDelay
}

// killProcess stops the app-server and everything left in its process group.
// Closing the context alone is not enough: exec only runs Cancel if it sees
// the context end before it observes the app-server exit.
func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
