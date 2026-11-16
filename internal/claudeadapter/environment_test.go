package claudeadapter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func recordTestEnvironment() {
	path := os.Getenv("CLAUDE_TEST_ENV_RECORD")
	if path == "" {
		return
	}
	var names []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	raw, _ := json.Marshal(names)
	_ = os.WriteFile(path, raw, 0600)
}

func TestClaudeChildDoesNotInheritControlPlaneEnvironment(t *testing.T) {
	for _, name := range []string{"WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN", "wio_private_setting"} {
		t.Setenv(name, "parent-only")
	}
	t.Setenv("CLAUDE_TEST_CHILD", "success")
	record := filepath.Join(t.TempDir(), "environment.json")
	t.Setenv("CLAUDE_TEST_ENV_RECORD", record)
	t.Setenv("CLAUDE_TEST_RECORD", filepath.Join(t.TempDir(), "input.json"))
	binary, _ := os.Executable()
	if err := Run(context.Background(), binary, "operation", protocol.ClaudeTurnCommand{Workspace: t.TempDir(), PermissionMode: "default"}, func(protocol.StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(strings.ToUpper(name), "WIO_") {
			t.Fatalf("inherited Wio setting reached Claude: %s", name)
		}
	}
}

func TestConfigureEnvironmentFiltersEvenWithoutOverrides(t *testing.T) {
	t.Setenv("WIO_MASTER_KEY", "parent-only")
	cmd := &exec.Cmd{}
	release, err := configureEnvironment(context.Background(), cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if cmd.Env == nil {
		t.Fatal("an unset Env would inherit the parent")
	}
	for _, entry := range cmd.Env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "WIO_") {
			t.Fatalf("inherited Wio setting reached empty-profile command: %s", name)
		}
	}
}
