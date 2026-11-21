//go:build linux || darwin

package installer

import (
	"github.com/wio-platform/wio/internal/childprocess"
	"os/exec"
	"syscall"
)

func configureInstallerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = childprocess.WaitDelay
}
