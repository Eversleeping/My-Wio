package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
)

func (c *Client) installCLI(ctx context.Context, command protocol.CLIInstallCommand) error {
	if !codexcli.ValidTargetVersion(command.Version) {
		return errors.New("invalid AI CLI target version")
	}
	switch command.Kind {
	case "codex":
		return c.updateCodexCLI(ctx, protocol.CodexUpdateCommand{Version: command.Version})
	case "gemini":
		executable := managedGeminiExecutable(c.config.StateDir, command.Version)
		return c.gemini.ReconfigureCommand(executable, func() error {
			if err := installGeminiCLI(ctx, c.config.StateDir, command.Version); err != nil {
				return err
			}
			return writeManagedCLIVersion(c.config.StateDir, "gemini", command.Version)
		})
	default:
		return errors.New("unsupported AI CLI")
	}
}

func installGeminiCLI(ctx context.Context, stateDir, version string) error {
	if !codexcli.ValidTargetVersion(version) {
		return errors.New("invalid Gemini CLI version")
	}
	if _, err := findCodexUpdateCommand("npm"); err != nil {
		return errors.New("Node.js 20+ and npm are required; use SSH repair to install server dependencies")
	}
	prefix := managedGeminiPrefix(stateDir, version)
	if err := os.MkdirAll(prefix, 0o750); err != nil {
		return err
	}
	if err := runCodexUpdateCommand(ctx, "npm", "install", "--prefix", prefix, "--omit=dev", "--registry=https://registry.npmjs.org", "@google/gemini-cli@"+version); err != nil {
		return fmt.Errorf("install Gemini CLI %s: %w", version, err)
	}
	if reported := readCodexUpdateVersion(ctx, managedGeminiExecutable(stateDir, version)); !codexcli.ReportedVersionMatches(reported, version) {
		return errors.New("Gemini CLI version verification failed")
	}
	return nil
}

func managedGeminiExecutable(stateDir, version string) string {
	return filepath.Join(managedGeminiPrefix(stateDir, version), "node_modules", ".bin", "gemini")
}

func managedGeminiPrefix(stateDir, version string) string {
	return filepath.Join(stateDir, "gemini", "versions", version)
}

func effectiveGeminiPath(stateDir string) string {
	raw, err := os.ReadFile(filepath.Join(stateDir, "gemini", codexCurrentVersionFile))
	version := strings.TrimSpace(string(raw))
	if err != nil || !codexcli.ValidTargetVersion(version) {
		return "gemini"
	}
	executable := managedGeminiExecutable(stateDir, version)
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		return "gemini"
	}
	return executable
}
