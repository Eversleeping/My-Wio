//go:build windows && platformintegration

package agent

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/geminiadapter"
	"github.com/wio-platform/wio/internal/protocol"
)

// Install the exact upstream releases which changed their npm bin entries.
// This opt-in check runs only on disposable native CI hosts. --version never
// invokes a model, and no production credentials or services are involved.
func TestWindowsNPMCLIInstall(t *testing.T) {
	if os.Getenv("WIO_TEST_WINDOWS_NPM") != "1" {
		t.Skip("set WIO_TEST_WINDOWS_NPM=1 to download and verify official CLI packages")
	}
	if _, err := exec.LookPath("node.exe"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	state := t.TempDir()
	c := &Client{config: Config{StateDir: state}, gemini: geminiadapter.New("gemini", nil)}
	for _, command := range []protocol.CLIInstallCommand{{Kind: "gemini", Version: "0.70.0"}, {Kind: "claude", Version: "2.1.330"}} {
		t.Run(command.Kind, func(t *testing.T) {
			if err := c.installCLI(ctx, command); err != nil {
				exe := managedGeminiExecutable(state, command.Version)
				if command.Kind == "claude" {
					exe = managedClaudeExecutable(state, command.Version)
				}
				output, probeErr := childprocess.CombinedOutput(childprocess.CommandContext(ctx, exe, "--version"))
				t.Fatalf("install %s: %v; version probe: %v\n%s", command.Kind, err, probeErr, output)
			}
			if command.Kind == "gemini" && effectiveGeminiPath(state) != c.gemini.Command() {
				t.Fatal("Gemini selection did not persist")
			}
			if command.Kind == "claude" && effectiveClaudePath(state) != managedClaudeExecutable(state, command.Version) {
				t.Fatal("Claude selection did not persist")
			}
		})
	}
}
