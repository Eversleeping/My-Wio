package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCodexEnvironmentIsScopedFromKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "codex.key")
	if err := os.WriteFile(keyFile, []byte("api-key-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	environment := codexEnvironment(Config{CodexAPIKeyFile: keyFile}, log)
	if len(environment) != 1 || environment[0] != "WIO_CODEX_API_KEY=api-key-value" {
		t.Fatalf("unexpected environment: %#v", environment)
	}
	if environment := codexEnvironment(Config{CodexAPIKeyFile: filepath.Join(t.TempDir(), "missing")}, log); environment != nil {
		t.Fatalf("expected no environment, got %#v", environment)
	}
}

func TestRedeliveredOperationReplaysCachedResult(t *testing.T) {
	client := &Client{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, 2),
		seen:     make(map[string]*operationExecution),
	}
	operation := &protocol.ControlEnvelope{OperationID: "operation-1", Kind: "unsupported"}
	client.handleOperation(context.Background(), operation)
	first := <-client.outbound
	client.handleOperation(context.Background(), operation)
	second := <-client.outbound
	for index, envelope := range []*protocol.AgentEnvelope{first, second} {
		var result protocol.OperationResult
		if envelope.Kind != "operation_result" || json.Unmarshal(envelope.PayloadJSON, &result) != nil || result.OperationID != operation.OperationID || result.Status != "failed" {
			t.Fatalf("unexpected replay %d: %#v payload=%s", index, envelope, envelope.PayloadJSON)
		}
	}
}

func TestSuccessfulConnectionResetsReconnectBackoff(t *testing.T) {
	if backoff := reconnectBackoffAfterResult(32*time.Second, true); backoff != time.Second {
		t.Fatalf("expected successful connection to reset backoff, got %s", backoff)
	}
	if backoff := reconnectBackoffAfterResult(16*time.Second, false); backoff != 16*time.Second {
		t.Fatalf("failed connection should preserve backoff, got %s", backoff)
	}
}

func TestInventoryRootsIncludeCloneRoot(t *testing.T) {
	base := t.TempDir()
	scanRoot := filepath.Join(base, "services")
	cloneRoot := filepath.Join(base, "state", "projects")
	client := &Client{config: Config{ScanRoots: []string{scanRoot, scanRoot}, CloneRoot: cloneRoot}}
	roots := client.inventoryRoots()
	if len(roots) != 2 || roots[0] != scanRoot || roots[1] != cloneRoot {
		t.Fatalf("unexpected inventory roots: %#v", roots)
	}
}

func TestInventoryRootsDoNotDuplicateCoveredCloneRoot(t *testing.T) {
	base := t.TempDir()
	cloneRoot := filepath.Join(base, "state", "projects")
	client := &Client{config: Config{ScanRoots: []string{base}, CloneRoot: cloneRoot}}
	roots := client.inventoryRoots()
	if len(roots) != 1 || roots[0] != base {
		t.Fatalf("unexpected inventory roots: %#v", roots)
	}
}
