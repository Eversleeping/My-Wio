//go:build windows

package codexadapter

import (
	"os/exec"

	"github.com/wio-platform/wio/internal/childprocess"
)

func configureProcess(cmd *exec.Cmd) { childprocess.Configure(cmd) }

func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = childprocess.KillTree(cmd)
	}
}
