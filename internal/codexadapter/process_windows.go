//go:build windows

package codexadapter

import (
	"os/exec"

	"github.com/wio-platform/wio/internal/childprocess"
)

func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = childprocess.WaitDelay }

func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
