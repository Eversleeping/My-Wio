//go:build windows

package claudeadapter

import (
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/hostplatform"
	"os"
	"os/exec"
	"path/filepath"
)

func configureProcess(cmd *exec.Cmd) { childprocess.Configure(cmd) }

// apiKeyDescriptor reports that Windows cannot pass the key through an
// inherited descriptor; the key stays in the CLI environment there.
func apiKeyDescriptor(*exec.Cmd, string) (string, func(), error) { return "", func() {}, nil }

// Windows uses a private file because inherited /dev/fd is unavailable.
// The containing directory's ACL is restricted before any headers are written.
func mcpConfigArgument(_ *exec.Cmd, configuration []byte) (string, func(), error) {
	directory, err := os.MkdirTemp("", "wio-mcp-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := hostplatform.ProtectUserDirectory(directory); err != nil {
		cleanup()
		return "", nil, err
	}
	file := filepath.Join(directory, "config.json")
	if err := os.WriteFile(file, configuration, 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return file, cleanup, nil
}
