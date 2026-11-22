package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCLIInventorySeedsVerifiedManagedSelections(t *testing.T) {
	state := t.TempDir()
	for _, cli := range []struct{ kind, version string }{{"codex", "0.171.0"}, {"gemini", "0.70.0"}, {"claude", "2.1.330"}} {
		path := managedCLIExecutable(filepath.Join(state, cli.kind, "versions", cli.version), cli.kind)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, cli.kind, codexCurrentVersionFile), []byte(cli.version+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := NewClient(Config{StateDir: state, CodexPath: "wio-nonexistent-test-cli"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer c.codex.Close()
	if err := c.enqueueHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var heartbeat protocol.Heartbeat
	if err := json.Unmarshal((<-c.outbound).PayloadJSON, &heartbeat); err != nil {
		t.Fatal(err)
	}
	if !heartbeat.CodexReady || !heartbeat.GeminiReady || !heartbeat.ClaudeReady || heartbeat.CodexVersion != "codex-cli 0.171.0" || heartbeat.GeminiVersion != "0.70.0" || heartbeat.ClaudeVersion != "2.1.330 (Claude Code)" {
		t.Fatalf("managed selections missing from initial heartbeat: %+v", heartbeat)
	}
}

func TestCLIInventoryTimeoutPreservesLastKnownState(t *testing.T) {
	c := NewClient(Config{StateDir: t.TempDir(), CodexPath: "wio-nonexistent-test-cli"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer c.codex.Close()
	c.updateCLIInventory(context.Background(), "codex", cliState{"codex-cli 0.171.0", true})
	old := readCodexUpdateVersion
	defer func() { readCodexUpdateVersion = old }()
	readCodexUpdateVersion = func(ctx context.Context, _ string) string { <-ctx.Done(); return "" }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.refreshCLIInventory(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := c.enqueueHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	var heartbeat protocol.Heartbeat
	if err := json.Unmarshal((<-c.outbound).PayloadJSON, &heartbeat); err != nil {
		t.Fatal(err)
	}
	if heartbeat.CodexVersion != "codex-cli 0.171.0" || !heartbeat.CodexReady {
		t.Fatal("timed-out probe erased the last known CLI state")
	}
}
