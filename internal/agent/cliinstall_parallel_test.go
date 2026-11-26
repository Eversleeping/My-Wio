package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCLIInstallRunsDifferentEnginesConcurrently(t *testing.T) {
	originalRun, originalFind := runCodexUpdateCommand, findCodexUpdateCommand
	originalCodex, originalGemini, originalClaude := readCodexUpdateVersion, readGeminiUpdateVersion, readClaudeUpdateVersion
	defer func() {
		runCodexUpdateCommand, findCodexUpdateCommand = originalRun, originalFind
		readCodexUpdateVersion, readGeminiUpdateVersion, readClaudeUpdateVersion = originalCodex, originalGemini, originalClaude
	}()
	started := make(chan string, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	defer func() { unblock(); workers.Wait() }()
	findCodexUpdateCommand = func(string) (string, error) { return "npm", nil }
	runCodexUpdateCommand = func(ctx context.Context, _ string, args ...string) error {
		started <- args[2] // Separate --prefix directories must be active simultaneously.
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	readCodexUpdateVersion = func(context.Context, string) string { return "1.0.0" }
	readGeminiUpdateVersion, readClaudeUpdateVersion = readCodexUpdateVersion, readCodexUpdateVersion
	c := NewClient(Config{StateDir: t.TempDir(), CodexPath: "missing-codex"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 3)
	for _, kind := range []string{"codex", "gemini", "claude"} {
		payload, _ := json.Marshal(protocol.CLIInstallCommand{Kind: kind, Version: "1.0.0"})
		workers.Add(1)
		go func() {
			defer workers.Done()
			done <- c.executeCLIInstall(ctx, &protocol.ControlEnvelope{PayloadJSON: payload})
		}()
	}
	prefixes := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case prefix := <-started:
			prefixes[prefix] = true
		case <-ctx.Done():
			t.Fatal("different engines did not install concurrently")
		}
	}
	if len(prefixes) != 3 {
		t.Fatal("installations shared a prefix", prefixes)
	}
	payload, _ := json.Marshal(protocol.CLIInstallCommand{Kind: "codex", Version: "1.0.0"})
	if err := c.executeCLIInstall(ctx, &protocol.ControlEnvelope{PayloadJSON: payload}); err == nil {
		t.Fatal("duplicate Codex install accepted")
	}
	if err := c.executeCodexUpdate(ctx, &protocol.ControlEnvelope{PayloadJSON: json.RawMessage(`{"version":"1.0.0"}`)}); err == nil {
		t.Fatal("legacy Codex update bypassed lock")
	}
	unblock()
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
