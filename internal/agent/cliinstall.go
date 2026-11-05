package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
)

var readGeminiUpdateVersion = func(ctx context.Context, executable string) string {
	return commandVersionWithTimeout(ctx, executable, 15*time.Second)
}

func (c *Client) currentGeminiVersion(ctx context.Context, executable string) string {
	c.geminiVersionMu.Lock()
	defer c.geminiVersionMu.Unlock()
	if c.geminiVersionPath == executable && time.Now().Before(c.geminiVersionUntil) {
		return c.geminiVersion
	}
	version := readGeminiUpdateVersion(ctx, executable)
	if version != "" {
		c.geminiVersionPath, c.geminiVersion, c.geminiVersionUntil = executable, version, time.Now().Add(2*time.Minute)
	}
	return version
}

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
	case "claude":
		if !c.claudeMu.TryLock() {
			return errors.New("finish active Claude turns before changing versions")
		}
		defer c.claudeMu.Unlock()
		if err := installClaudeCLI(ctx, c.config.StateDir, command.Version); err != nil {
			return err
		}
		return writeManagedCLIVersion(c.config.StateDir, "claude", command.Version)
	default:
		return errors.New("unsupported AI CLI")
	}
}

var readClaudeUpdateVersion = func(ctx context.Context, executable string) string {
	return commandVersionWithTimeout(ctx, executable, 15*time.Second)
}

func (c *Client) currentClaudeVersion(ctx context.Context, executable string) string {
	c.claudeVersionMu.Lock()
	defer c.claudeVersionMu.Unlock()
	if c.claudeVersionPath == executable && time.Now().Before(c.claudeVersionUntil) {
		return c.claudeVersion
	}
	version := readClaudeUpdateVersion(ctx, executable)
	if version != "" {
		c.claudeVersionPath, c.claudeVersion, c.claudeVersionUntil = executable, version, time.Now().Add(2*time.Minute)
	}
	return version
}

func installClaudeCLI(ctx context.Context, stateDir, version string) error {
	if !codexcli.ValidTargetVersion(version) {
		return errors.New("invalid Claude CLI version")
	}
	if _, err := findCodexUpdateCommand("npm"); err != nil {
		return errors.New("Node.js and npm are required; use SSH repair to install server dependencies")
	}
	prefix := managedClaudePrefix(stateDir, version)
	if err := os.MkdirAll(prefix, 0o750); err != nil {
		return err
	}
	if err := runCodexUpdateCommand(ctx, "npm", "install", "--prefix", prefix, "--omit=dev", "--registry=https://registry.npmjs.org", "@anthropic-ai/claude-code@"+version); err != nil {
		return fmt.Errorf("install Claude CLI %s: %w", version, err)
	}
	if reported := readClaudeUpdateVersion(ctx, managedClaudeExecutable(stateDir, version)); !codexcli.ReportedVersionMatches(reported, version) {
		return errors.New("Claude CLI version verification failed")
	}
	return nil
}

func managedClaudePrefix(stateDir, version string) string {
	return filepath.Join(stateDir, "claude", "versions", version)
}

func managedClaudeExecutable(stateDir, version string) string {
	return filepath.Join(managedClaudePrefix(stateDir, version), "node_modules", ".bin", "claude")
}

func effectiveClaudePath(stateDir string) string {
	raw, err := os.ReadFile(filepath.Join(stateDir, "claude", codexCurrentVersionFile))
	version := strings.TrimSpace(string(raw))
	if err != nil || !codexcli.ValidTargetVersion(version) {
		return "claude"
	}
	executable := managedClaudeExecutable(stateDir, version)
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		return "claude"
	}
	return executable
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
	if reported := readGeminiUpdateVersion(ctx, managedGeminiExecutable(stateDir, version)); !codexcli.ReportedVersionMatches(reported, version) {
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
