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

func interruptTestClient(t *testing.T) *Client {
	t.Helper()
	return &Client{config: Config{StateDir: t.TempDir()}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), seen: make(map[string]*operationExecution), outbound: make(chan *protocol.AgentEnvelope, 32)}
}

func TestOperationInterruptWaitsForExecutionAndPreservesReceipt(t *testing.T) {
	c := interruptTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	execution := &operationExecution{kind: "git.workspace.inspect", cancel: cancel, done: make(chan struct{})}
	c.seen["running"] = execution
	finished := make(chan error, 1)
	command := protocol.OperationInterruptCommand{OperationID: "running", Kind: execution.kind}
	go func() { finished <- c.interruptOperation(context.Background(), command) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("execution was not cancelled")
	}
	select {
	case err := <-finished:
		t.Fatalf("acknowledged before execution finished: %v", err)
	default:
	}
	// Completion can win the race with cancellation. Replay the actual receipt.
	c.seenMu.Lock()
	execution.result = protocol.OperationResult{OperationID: "running", Status: "succeeded", Message: "finished first"}
	close(execution.done)
	c.seenMu.Unlock()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		message := receiveAgentEnvelope(t, c.outbound)
		var result protocol.OperationResult
		if err := json.Unmarshal(message.PayloadJSON, &result); err != nil || result.Status != "succeeded" || result.Message != "finished first" {
			t.Fatalf("completion overwritten: %s %v", message.PayloadJSON, err)
		}
		if attempt == 0 {
			if err := c.interruptOperation(context.Background(), command); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOperationInterruptTimeoutDoesNotClaimTermination(t *testing.T) {
	c := interruptTestClient(t)
	c.seen["stuck"] = &operationExecution{kind: "workspace.files", cancel: func() {}, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := c.interruptOperation(ctx, protocol.OperationInterruptCommand{OperationID: "stuck", Kind: "workspace.files"}); err == nil {
		t.Fatal("unconfirmed execution reported as stopped")
	}
	if len(c.outbound) != 0 {
		t.Fatal("unconfirmed execution emitted a result")
	}
}

func TestOperationInterruptTombstonePreventsDelayedExecutionAfterRestart(t *testing.T) {
	c := interruptTestClient(t)
	command := protocol.OperationInterruptCommand{OperationID: "delayed", Kind: "git.workspace.inspect"}
	if err := c.interruptOperation(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	receiveAgentEnvelope(t, c.outbound)
	for _, client := range []*Client{c, {config: c.config, seen: make(map[string]*operationExecution), outbound: make(chan *protocol.AgentEnvelope, 8)}} {
		client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: command.OperationID, Kind: command.Kind, PayloadJSON: json.RawMessage(`{}`)})
		message := receiveAgentEnvelope(t, client.outbound)
		var result protocol.OperationResult
		if err := json.Unmarshal(message.PayloadJSON, &result); err != nil || message.Kind != "operation_result" || result.Status != "cancelled" {
			t.Fatalf("delayed command executed: %s %v", message.PayloadJSON, err)
		}
	}
}

func TestOperationInterruptRejectsUnsafeKindsAndUnreadableReceipts(t *testing.T) {
	c := interruptTestClient(t)
	for _, kind := range []string{"deploy.start", "agent.update", "codex.turn.start", "claude.turn.start", "workspace.file.write"} {
		if err := c.interruptOperation(context.Background(), protocol.OperationInterruptCommand{OperationID: "unsafe", Kind: kind}); err == nil {
			t.Fatalf("accepted unsafe kind %s", kind)
		}
	}
	c.seen["mismatch"] = &operationExecution{kind: "workspace.file.write"}
	if err := c.interruptOperation(context.Background(), protocol.OperationInterruptCommand{OperationID: "mismatch", Kind: "workspace.files"}); err == nil {
		t.Fatal("accepted mismatched target")
	}
	if err := os.MkdirAll(filepath.Dir(c.operationResultPath("corrupt")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.operationResultPath("corrupt"), []byte("invalid receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.interruptOperation(context.Background(), protocol.OperationInterruptCommand{OperationID: "corrupt", Kind: "workspace.files"}); err == nil {
		t.Fatal("unverifiable completion reported as cancelled")
	}
}

func TestOperationInterruptCancelledScanReportsCancelled(t *testing.T) {
	c := interruptTestClient(t)
	root := t.TempDir()
	c.config.ScanRoots = []string{root}
	payload, err := json.Marshal(protocol.WorkspaceFilesCommand{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.handleOperation(ctx, &protocol.ControlEnvelope{OperationID: "cancelled-scan", Kind: "workspace.files", PayloadJSON: payload})
	receiveOperationStarted(t, c.outbound, "cancelled-scan")
	message := receiveAgentEnvelope(t, c.outbound)
	var result protocol.OperationResult
	if err := json.Unmarshal(message.PayloadJSON, &result); err != nil || result.Status != "cancelled" {
		t.Fatalf("cancelled scan: %s %v", message.PayloadJSON, err)
	}
}

func TestOperationInterruptReplayRestoresTargetReceiptAfterRestart(t *testing.T) {
	c := interruptTestClient(t)
	command := protocol.OperationInterruptCommand{OperationID: "target-before-restart", Kind: "git.workspace.inspect"}
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	envelope := &protocol.ControlEnvelope{OperationID: "interrupt-before-restart", Kind: "operation.interrupt", PayloadJSON: payload}
	c.handleOperation(context.Background(), envelope)
	receiveOperationStarted(t, c.outbound, envelope.OperationID)
	// Simulate both receipts being lost during process shutdown.
	receiveAgentEnvelope(t, c.outbound)
	receiveAgentEnvelope(t, c.outbound)
	for _, client := range []*Client{c, {config: c.config, seen: make(map[string]*operationExecution), outbound: make(chan *protocol.AgentEnvelope, 8)}} {
		client.handleOperation(context.Background(), envelope)
		for i, id := range []string{command.OperationID, envelope.OperationID} {
			message := receiveAgentEnvelope(t, client.outbound)
			var result protocol.OperationResult
			if err := json.Unmarshal(message.PayloadJSON, &result); err != nil || result.OperationID != id || message.Kind != "operation_result" {
				t.Fatalf("receipt %d not restored: %s %v", i, message.PayloadJSON, err)
			}
			if (i == 0 && result.Status != "cancelled") || (i == 1 && result.Status != "succeeded") {
				t.Fatalf("wrong restored result: %#v", result)
			}
		}
	}
}
