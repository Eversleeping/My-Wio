package geminiadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func recordTestEnvironment() {
	path := os.Getenv("GEMINI_TEST_ENV_RECORD")
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

func TestGeminiChildrenDoNotInheritControlPlaneEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN", "wio_private_setting", "WIO_GEMINI_PARENT_SECRET"} {
		t.Setenv(name, "parent-only")
	}
	for _, interactive := range []bool{false, true} {
		name, mode := "stream", "success"
		if interactive {
			name, mode = "acp", "acp"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("GEMINI_TEST_CHILD", mode)
			record := filepath.Join(t.TempDir(), "environment.json")
			t.Setenv("GEMINI_TEST_ENV_RECORD", record)
			t.Setenv("GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "input.json"))
			t.Setenv("GEMINI_TEST_USAGE_RESULT", `{"stopReason":"end_turn"}`)
			binary, _ := os.Executable()
			a := New(binary, func(protocol.StreamEvent) error { return nil })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.Run(ctx, "operation", protocol.GeminiTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), Model: "flash", ApprovalMode: "default", Interactive: interactive}); err != nil {
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
				if name == "WIO_GEMINI_PARENT_SECRET" {
					t.Fatal("an inherited Gemini-prefixed Wio setting reached the CLI")
				}
				if strings.HasPrefix(strings.ToUpper(name), "WIO_") && !strings.HasPrefix(name, "WIO_GEMINI_") {
					t.Fatalf("inherited Wio setting reached Gemini: %s", name)
				}
			}
		})
	}
}
