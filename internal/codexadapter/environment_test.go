package codexadapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func recordTestEnvironment() {
	path := os.Getenv("CODEX_TEST_ENV_RECORD")
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

func TestCodexChildFiltersControlPlaneAndKeepsExplicitCredentials(t *testing.T) {
	for _, name := range []string{"WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN", "wio_private_setting", "WIO_CODEX_API_KEY"} {
		t.Setenv(name, "parent-only")
	}
	record := filepath.Join(t.TempDir(), "environment.json")
	binary, _ := os.Executable()
	a := NewWithEnvironment(binary, []string{"WIO_TEST_SESSION_SERVER=1", "CODEX_TEST_ENV_RECORD=" + record, "WIO_CODEX_API_KEY=selected-key"}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(protocol.StreamEvent) error { return nil })
	defer a.Close()
	if err := a.Warm(context.Background()); err != nil {
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
		if strings.HasPrefix(strings.ToUpper(name), "WIO_") && name != "WIO_TEST_SESSION_SERVER" && name != "WIO_CODEX_API_KEY" {
			t.Fatalf("inherited Wio setting reached Codex: %s", name)
		}
	}
	if !slices.Contains(names, "WIO_CODEX_API_KEY") {
		t.Fatal("explicit provider credential was lost")
	}
}
