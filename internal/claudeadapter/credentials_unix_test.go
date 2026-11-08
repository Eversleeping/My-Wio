//go:build !windows

package claudeadapter

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

// recordAPIKeyDelivery runs in the fake CLI and records how the API key
// arrived: through the environment, or through the inherited descriptor.
func recordAPIKeyDelivery(path string) {
	delivery := map[string]string{"environment": os.Getenv("ANTHROPIC_API_KEY"), "descriptor": os.Getenv(apiKeyDescriptorVariable)}
	if fd, err := strconv.Atoi(delivery["descriptor"]); err == nil {
		file := os.NewFile(uintptr(fd), "claude-api-key")
		raw, _ := io.ReadAll(file)
		_ = file.Close()
		delivery["key"] = string(raw)
	}
	raw, _ := json.Marshal(delivery)
	_ = os.WriteFile(path, raw, 0o600)
}

// The Bash tool and every process Claude starts inherit the CLI environment,
// so the profile key must reach the CLI only through the descriptor.
func TestClaudeAPIKeyReachesCLIOnlyThroughDescriptor(t *testing.T) {
	directory := t.TempDir()
	keyRecord := filepath.Join(directory, "key.json")
	t.Setenv("WIO_CLAUDE_TEST_CHILD", "success")
	t.Setenv("WIO_CLAUDE_TEST_RECORD", filepath.Join(directory, "record.json"))
	t.Setenv("WIO_CLAUDE_TEST_KEY_RECORD", keyRecord)
	// A key in the Agent's own environment must not leak in either.
	t.Setenv("ANTHROPIC_API_KEY", "inherited-agent-key")
	t.Setenv(apiKeyDescriptorVariable, "9")
	binary, _ := os.Executable()
	command := protocol.ClaudeTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), Prompt: "hello", PermissionMode: "default"}
	if err := Run(context.Background(), binary, "operation", command, func(protocol.StreamEvent) error { return nil }, "DISABLE_AUTOUPDATER=1", "ANTHROPIC_API_KEY=profile-secret", "ANTHROPIC_BASE_URL=https://api.example.test"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(keyRecord)
	if err != nil {
		t.Fatal(err)
	}
	var delivery map[string]string
	if err := json.Unmarshal(raw, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery["environment"] != "" || delivery["descriptor"] != "3" || delivery["key"] != "profile-secret" {
		t.Fatalf("API key delivery = %#v, want only descriptor 3 carrying the profile key", delivery)
	}
}

func TestConfigureEnvironmentKeepsEmptyKeyAndReleasesDescriptor(t *testing.T) {
	cmd := newTestCommand()
	release, err := configureEnvironment(cmd, []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN="})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(cmd.ExtraFiles) != 0 || !containsEntry(cmd.Env, "ANTHROPIC_API_KEY=") {
		t.Fatalf("an empty key must stay a plain override: %v %d", cmd.Env, len(cmd.ExtraFiles))
	}
	cmd = newTestCommand()
	release, err = configureEnvironment(cmd, []string{"ANTHROPIC_API_KEY=profile-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.ExtraFiles) != 1 || containsEntry(cmd.Env, "ANTHROPIC_API_KEY=profile-secret") || !containsEntry(cmd.Env, apiKeyDescriptorVariable+"=3") {
		t.Fatalf("key was not moved to a descriptor: %v", cmd.Env)
	}
	release()
	if _, err := cmd.ExtraFiles[0].Stat(); err == nil {
		t.Fatal("release left the parent's descriptor open")
	}
}

func newTestCommand() *exec.Cmd { return exec.Command("true") }

func containsEntry(environment []string, entry string) bool {
	for _, candidate := range environment {
		if candidate == entry {
			return true
		}
	}
	return false
}
