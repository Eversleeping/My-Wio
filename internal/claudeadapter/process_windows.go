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

// Windows has no inherited /dev/fd file interface for --mcp-config. Preserve
// the CLI's inline JSON configuration there; headers remain visible in argv.
func mcpConfigArgument(_ *exec.Cmd, configuration []byte) (string, func(), error) {
	return string(configuration), func() {}, nil
}
