//go:build windows

package claudeadapter

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }

// apiKeyDescriptor reports that Windows cannot pass the key through an
// inherited descriptor; the key stays in the CLI environment there.
func apiKeyDescriptor(*exec.Cmd, string) (string, func(), error) { return "", func() {}, nil }
