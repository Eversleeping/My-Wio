package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
)

const codexCurrentVersionFile = "current-version"

var runCodexUpdateCommand = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}
var findCodexUpdateCommand = exec.LookPath
var readCodexUpdateVersion = commandVersion

func (c *Client) updateCodexCLI(ctx context.Context, command protocol.CodexUpdateCommand) error {
	version := strings.TrimSpace(command.Version)
	if !codexcli.ValidTargetVersion(version) {
		return errors.New("invalid Codex CLI target version")
	}
	executable := managedCodexExecutable(c.config.StateDir, version)
	if err := c.codex.ReconfigureCommand(executable, func() error {
		if err := installCodexCLI(ctx, c.config.StateDir, version); err != nil {
			return err
		}
		return writeManagedCodexVersion(c.config.StateDir, version)
	}); err != nil {
		return err
	}
	c.setCodexPath(executable)
	return nil
}

func installCodexCLI(ctx context.Context, stateDir, version string) error {
	if !codexcli.ValidTargetVersion(version) {
		return errors.New("invalid Codex CLI target version")
	}
	if _, err := findCodexUpdateCommand("npm"); err != nil {
		return errors.New("npm is required to update Codex CLI")
	}
	prefix := managedCodexPrefix(stateDir, version)
	if err := os.MkdirAll(prefix, 0o750); err != nil {
		return fmt.Errorf("prepare Codex CLI directory: %w", err)
	}
	if err := runCodexUpdateCommand(ctx, "npm", "install", "--prefix", prefix, "--omit=dev", "@openai/codex@"+version); err != nil {
		return fmt.Errorf("install Codex CLI %s: %w", version, err)
	}
	executable := managedCodexExecutable(stateDir, version)
	if reported := readCodexUpdateVersion(ctx, executable); !codexcli.ReportedVersionMatches(reported, version) {
		return fmt.Errorf("Codex CLI verification failed for version %s", version)
	}
	return nil
}

func effectiveCodexPath(config Config) string {
	raw, err := os.ReadFile(filepath.Join(config.StateDir, "codex", codexCurrentVersionFile))
	if err != nil {
		return config.CodexPath
	}
	version := strings.TrimSpace(string(raw))
	if !codexcli.ValidTargetVersion(version) {
		return config.CodexPath
	}
	executable := managedCodexExecutable(config.StateDir, version)
	if info, statErr := os.Stat(executable); statErr != nil || info.IsDir() {
		return config.CodexPath
	}
	return executable
}

func managedCodexPrefix(stateDir, version string) string {
	return filepath.Join(stateDir, "codex", "versions", version)
}

func managedCodexExecutable(stateDir, version string) string {
	return filepath.Join(managedCodexPrefix(stateDir, version), "node_modules", ".bin", "codex")
}

func writeManagedCodexVersion(stateDir, version string) error {
	root := filepath.Join(stateDir, "codex")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(root, ".current-version-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(version + "\n"); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(root, codexCurrentVersionFile))
}
