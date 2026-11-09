package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCompletedCacheBoundsAndProtectsActiveOperations(t *testing.T) {
	now := time.Now()
	c := &Client{seen: map[string]*operationExecution{}}
	active := &operationExecution{done: make(chan struct{})}
	c.seen["active"] = active
	c.seen["unpersisted"] = &operationExecution{completedAt: now.Add(-2 * time.Hour)}
	for i := 0; i < 200; i++ {
		c.seen[fmt.Sprint(i)] = &operationExecution{persisted: true, completedAt: now.Add(-time.Duration(i) * time.Second)}
	}
	c.pruneCompletedOperations(now)
	if len(c.seen) != completedOperationLimit+2 || c.seen["active"] != active {
		t.Fatalf("unexpected cache size: %d", len(c.seen))
	}
	c.seen["huge"] = &operationExecution{persisted: true, completedAt: now, result: protocol.OperationResult{Data: json.RawMessage(make([]byte, completedOperationByteLimit+1))}}
	c.pruneCompletedOperations(now)
	if c.seen["huge"] != nil {
		t.Fatal("byte limit not enforced")
	}
	c.seen["expired"] = &operationExecution{persisted: true, completedAt: now.Add(-2 * time.Hour)}
	c.pruneCompletedOperations(now)
	if c.seen["expired"] != nil {
		t.Fatal("expired result retained")
	}
}

func TestReceiptReplaysAfterEvictionAndRestart(t *testing.T) {
	config := Config{StateDir: t.TempDir()}
	newClient := func() *Client {
		return &Client{config: config, log: slog.New(slog.NewTextHandler(io.Discard, nil)), seen: map[string]*operationExecution{}, outbound: make(chan *protocol.AgentEnvelope, 4)}
	}
	c := newClient()
	op := &protocol.ControlEnvelope{OperationID: "receipt-op", Kind: "unsupported"}
	c.handleOperation(context.Background(), op)
	receiveOperationStarted(t, c.outbound, op.OperationID)
	original := receiveAgentEnvelope(t, c.outbound)
	c.seenMu.Lock()
	c.pruneCompletedOperations(time.Now().Add(2 * time.Hour))
	c.seenMu.Unlock()
	for _, client := range []*Client{c, newClient()} {
		client.handleOperation(context.Background(), op)
		replay := receiveAgentEnvelope(t, client.outbound)
		if replay.Kind != "operation_result" || string(replay.PayloadJSON) != string(original.PayloadJSON) {
			t.Fatal("operation executed instead of replaying receipt")
		}
	}
	if err := os.WriteFile(c.operationResultPath(op.OperationID), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	c.handleOperation(context.Background(), op)
	var result protocol.OperationResult
	envelope := receiveAgentEnvelope(t, c.outbound)
	if err := json.Unmarshal(envelope.PayloadJSON, &result); err != nil || result.Message != "could not read previous operation result; refusing to repeat operation" {
		t.Fatalf("corrupt receipt did not fail closed: %+v", result)
	}
}

func TestPruneOperationReceiptsExpiresOnlyManagedOldFiles(t *testing.T) {
	now := time.Now()
	c := &Client{config: Config{StateDir: t.TempDir()}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, id := range []string{"old", "fresh"} {
		if err := c.saveOperationResult(protocol.OperationResult{OperationID: id, Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-operationReceiptRetention - time.Hour)
	if err := os.Chtimes(c.operationResultPath("old"), old, old); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(c.operationResultPath("old"))
	for _, name := range []string{".receipt-abandoned", "unmanaged.json.gz"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("untouched"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	c.pruneOperationReceipts(now)
	for _, path := range []string{c.operationResultPath("old"), filepath.Join(root, ".receipt-abandoned")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired file was retained: %s %v", path, err)
		}
	}
	if _, found, err := c.loadOperationResult("fresh"); !found || err != nil {
		t.Fatalf("fresh receipt was removed: %v %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(root, "unmanaged.json.gz")); err != nil {
		t.Fatal("cleanup removed an unmanaged file")
	}
}
