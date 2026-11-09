package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeStreamProxyRoutesOnlyCustomEndpoints(t *testing.T) {
	c := &Client{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Setenv("NO_PROXY", "internal.example")
	t.Setenv("no_proxy", "")
	routed, stop := c.claudeStreamProxy([]string{"DISABLE_AUTOUPDATER=1", "ANTHROPIC_BASE_URL=https://relay.example.com/api"})
	defer stop()
	if !strings.HasPrefix(routed[1], "ANTHROPIC_BASE_URL=http://127.0.0.1:") || routed[0] != "DISABLE_AUTOUPDATER=1" {
		t.Fatalf("custom endpoint not routed: %v", routed)
	}
	if joined := strings.Join(routed, "|"); !strings.Contains(joined, "NO_PROXY=internal.example,127.0.0.1,localhost") || !strings.Contains(joined, "no_proxy=127.0.0.1,localhost") {
		t.Fatalf("loopback not exempted from proxies: %v", routed)
	}
	for _, environment := range [][]string{{"ANTHROPIC_BASE_URL=https://api.anthropic.com"}, {"ANTHROPIC_BASE_URL="}, {"DISABLE_AUTOUPDATER=1"}} {
		kept, stop := c.claudeStreamProxy(environment)
		stop()
		if strings.Join(kept, "|") != strings.Join(environment, "|") {
			t.Fatalf("environment changed: %v -> %v", environment, kept)
		}
	}
	t.Setenv("WIO_CLAUDE_STREAM_PROXY", "off")
	kept, stop := c.claudeStreamProxy([]string{"ANTHROPIC_BASE_URL=https://relay.example.com"})
	stop()
	if kept[0] != "ANTHROPIC_BASE_URL=https://relay.example.com" {
		t.Fatalf("opt-out ignored: %v", kept)
	}
}

func TestClaudeForkSnapshotRemovedOnSuccessAndKeptOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")
	workspace := filepath.Join(tempDir, "workspace")
	version := "2.1.319"
	binDir := filepath.Join(stateDir, "claude", "versions", version, "node_modules", ".bin")
	forksDir := filepath.Join(stateDir, "claude", "forks")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(forksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "claude", "current-version"), []byte(version), 0644); err != nil {
		t.Fatal(err)
	}

	fakeClaude := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
    echo "2.1.319"
    exit 0
fi
if [ "$WIO_TEST_CLAUDE_FAIL" = "1" ]; then
    echo '{"type":"system","subtype":"init","session_id":"550e8400-e29b-41d4-a716-446655440001"}'
    echo "crash before completion" >&2
    exit 1
fi
echo '{"type":"system","subtype":"init","session_id":"550e8400-e29b-41d4-a716-446655440001"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
echo '{"type":"result","subtype":"success","uuid":"res-1"}'
exit 0
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	snapshotID := "550e8400-e29b-41d4-a716-446655440099"
	snapshotPath := filepath.Join(forksDir, snapshotID+".jsonl")

	newClient := func() *Client {
		return &Client{
			config:   Config{StateDir: stateDir, CloneRoot: tempDir},
			log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			seen:     make(map[string]*operationExecution),
			outbound: make(chan *protocol.AgentEnvelope, 100),
		}
	}

	cmdPayload, _ := json.Marshal(protocol.ClaudeTurnCommand{
		ThreadID:       snapshotID,
		ForkSnapshotID: snapshotID,
		Workspace:      workspace,
		Prompt:         "hello",
		PermissionMode: "default",
	})
	envelope := protocol.ControlEnvelope{
		OperationID: "op-1",
		Kind:        "claude.turn.start",
		PayloadJSON: cmdPayload,
	}

	// 1. Run fails: snapshot must be retained
	if err := os.WriteFile(snapshotPath, []byte("transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIO_TEST_CLAUDE_FAIL", "1")
	c := newClient()
	c.handleOperation(context.Background(), &envelope)
	if _, statErr := os.Stat(snapshotPath); os.IsNotExist(statErr) {
		t.Fatal("snapshot was prematurely deleted when Run failed")
	}

	// 2. Run succeeds: snapshot must be deleted
	t.Setenv("WIO_TEST_CLAUDE_FAIL", "0")
	c2 := newClient()
	envelope.OperationID = "op-2"
	c2.handleOperation(context.Background(), &envelope)
	for len(c2.outbound) > 0 {
		msg := <-c2.outbound
		if msg.Kind == "operation_result" {
			var res protocol.OperationResult
			_ = json.Unmarshal(msg.PayloadJSON, &res)
			if res.Status != "succeeded" {
				t.Fatalf("operation failed: %s %s", res.Status, res.Message)
			}
		}
	}
	if _, statErr := os.Stat(snapshotPath); !os.IsNotExist(statErr) {
		t.Fatalf("snapshot was not deleted after successful Run: %v", statErr)
	}
}
