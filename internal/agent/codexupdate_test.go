package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstallCodexCLIUsesFixedArguments(t *testing.T) {
	originalRun := runCodexUpdateCommand
	originalFind := findCodexUpdateCommand
	originalVersion := readCodexUpdateVersion
	t.Cleanup(func() {
		runCodexUpdateCommand = originalRun
		findCodexUpdateCommand = originalFind
		readCodexUpdateVersion = originalVersion
	})
	findCodexUpdateCommand = func(string) (string, error) { return "/usr/bin/npm", nil }
	var name string
	var arguments []string
	runCodexUpdateCommand = func(_ context.Context, command string, args ...string) error {
		name = command
		arguments = append([]string(nil), args...)
		return nil
	}
	readCodexUpdateVersion = func(context.Context, string) string { return "codex-cli 0.144.4" }
	stateDir := t.TempDir()
	if err := installCodexCLI(context.Background(), stateDir, "0.144.4"); err != nil {
		t.Fatal(err)
	}
	expected := []string{"install", "--prefix", managedCodexPrefix(stateDir, "0.144.4"), "--omit=dev", "@openai/codex@0.144.4"}
	if name != "npm" || !reflect.DeepEqual(arguments, expected) {
		t.Fatalf("unexpected command: %s %#v", name, arguments)
	}
}

func TestInstallCodexCLIRejectsUntrustedVersion(t *testing.T) {
	if err := installCodexCLI(context.Background(), t.TempDir(), "0.144.4; touch /tmp/pwned"); err == nil {
		t.Fatal("expected invalid version to be rejected")
	}
}

func TestEffectiveCodexPathUsesVerifiedManagedVersion(t *testing.T) {
	stateDir := t.TempDir()
	executable := managedCodexExecutable(stateDir, "0.144.4")
	if err := os.MkdirAll(filepath.Dir(executable), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedCodexVersion(stateDir, "0.144.4"); err != nil {
		t.Fatal(err)
	}
	if path := effectiveCodexPath(Config{StateDir: stateDir, CodexPath: "codex"}); path != executable {
		t.Fatalf("expected managed executable, got %q", path)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "codex", codexCurrentVersionFile), []byte("../../evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	if path := effectiveCodexPath(Config{StateDir: stateDir, CodexPath: "codex"}); path != "codex" {
		t.Fatalf("expected fallback executable, got %q", path)
	}
}

func TestFailedCodexInstallDoesNotChangeCurrentVersion(t *testing.T) {
	originalRun := runCodexUpdateCommand
	originalFind := findCodexUpdateCommand
	t.Cleanup(func() { runCodexUpdateCommand = originalRun; findCodexUpdateCommand = originalFind })
	findCodexUpdateCommand = func(string) (string, error) { return "/usr/bin/npm", nil }
	runCodexUpdateCommand = func(context.Context, string, ...string) error { return errors.New("install failed") }
	stateDir := t.TempDir()
	if err := writeManagedCodexVersion(stateDir, "0.139.0"); err != nil {
		t.Fatal(err)
	}
	if err := installCodexCLI(context.Background(), stateDir, "0.144.4"); err == nil {
		t.Fatal("expected install failure")
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "codex", codexCurrentVersionFile))
	if err != nil || string(raw) != "0.139.0\n" {
		t.Fatalf("current version changed after failure: %q %v", raw, err)
	}
}
