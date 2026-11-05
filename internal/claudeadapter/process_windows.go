//go:build windows

package claudeadapter

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }
