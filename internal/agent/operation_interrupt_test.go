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

func shortPreStartInterruptWait(t *testing.T) {
	t.Helper()
	previous := preStartInterruptWait
	preStartInterruptWait = 50 * time.Millisecond
	t.Cleanup(func() { preStartInterruptWait = previous })
}

type turnInterruptCase struct {
	name      string
	startKind string
	interrupt func(*Client, context.Context, string) error
	probe     func(*Client, string)
	lost      string
	payload   any
}

func turnInterruptCases(t *testing.T) []turnInterruptCase {
	return []turnInterruptCase{
		{
			name: "claude", startKind: "claude.turn.start", lost: protocol.ClaudeOwnershipLostMessage,
			interrupt: (*Client).interruptClaudeAndWait,
			probe: func(c *Client, id string) {
				raw, _ := json.Marshal(protocol.GeminiKeepalive{ClaudeOperationIDs: []string{id}})
				c.reconcileClaudeOperations(raw)
			},
			payload: protocol.ClaudeTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), Prompt: "must not run", PermissionMode: "default"},
		},
		{
			name: "gemini", startKind: "gemini.turn.start", lost: protocol.GeminiOwnershipLostMessage,
			interrupt: (*Client).interruptGeminiAndWait,
			probe: func(c *Client, id string) {
				raw, _ := json.Marshal(protocol.GeminiKeepalive{OperationIDs: []string{id}})
				c.reconcileGeminiOperations(raw)
			},
			payload: protocol.GeminiTurnCommand{ThreadID: "thread", Workspace: t.TempDir(), Prompt: "must not run", Model: "flash", ApprovalMode: "default"},
		},
	}
}

func waitForPreStartTombstone(t *testing.T, c *Client, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.seenMu.Lock()
		execution := c.seen[id]
		recorded := execution != nil && execution.awaitingStart()
		c.seenMu.Unlock()
		if recorded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("interrupt did not record a tombstone for the unregistered start")
		}
		time.Sleep(time.Millisecond)
	}
}

func expectInterruptedResult(t *testing.T, outbound <-chan *protocol.AgentEnvelope, id string) {
	t.Helper()
	message := receiveAgentEnvelope(t, outbound)
	var result protocol.OperationResult
	if message.Kind != "operation_result" || json.Unmarshal(message.PayloadJSON, &result) != nil || result.OperationID != id || result.Status != "interrupted" || result.Message != preStartInterruptMessage {
		t.Fatalf("late start was not answered with the interruption: kind=%s payload=%s", message.Kind, message.PayloadJSON)
	}
}

// Every envelope runs on its own goroutine, so an interrupt delivered right
// after its turn start can be handled first. Order it deterministically: the
// start must replay the interruption instead of running a whole turn.
func TestTurnInterruptBeforeStartCancelsTheLateStart(t *testing.T) {
	for _, tc := range turnInterruptCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			c := interruptTestClient(t)
			interrupted := make(chan error, 1)
			go func() { interrupted <- tc.interrupt(c, context.Background(), "turn") }()
			waitForPreStartTombstone(t, c, "turn")
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			c.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "turn", Kind: tc.startKind, PayloadJSON: payload})
			// No operation_started: the start finished before it could run.
			expectInterruptedResult(t, c.outbound, "turn")
			if err := <-interrupted; err != nil {
				t.Fatalf("interrupt of a suppressed start: %v", err)
			}
			result, found, err := c.loadOperationResult("turn")
			if err != nil || !found || result.Status != "interrupted" {
				t.Fatalf("suppressed start left no receipt: %#v %v %v", result, found, err)
			}
			// Probes and repeated interrupts now see a finished turn.
			tc.probe(c, "turn")
			expectInterruptedResult(t, c.outbound, "turn")
			if err := tc.interrupt(c, context.Background(), "turn"); err != nil {
				t.Fatalf("repeated interrupt: %v", err)
			}
			if len(c.outbound) != 0 {
				t.Fatalf("unexpected messages: %d", len(c.outbound))
			}
		})
	}
}

// A start that never reaches this process went to an earlier Agent process,
// whose CLI may still be running: that stays an ownership loss, while the
// tombstone still keeps a delayed start from running.
func TestTurnInterruptWithoutStartReportsOwnershipLost(t *testing.T) {
	shortPreStartInterruptWait(t)
	for _, tc := range turnInterruptCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			c := interruptTestClient(t)
			for attempt := 0; attempt < 2; attempt++ {
				if err := tc.interrupt(c, context.Background(), "orphan"); err == nil || err.Error() != tc.lost {
					t.Fatalf("attempt %d: unknown turn reported stopped: %v", attempt, err)
				}
			}
			tc.probe(c, "orphan")
			message := receiveAgentEnvelope(t, c.outbound)
			if message.Kind != tc.name+"_ownership_lost" {
				t.Fatalf("probe claimed a result for a turn this process never saw: %s %s", message.Kind, message.PayloadJSON)
			}
			payload, _ := json.Marshal(tc.payload)
			c.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "orphan", Kind: tc.startKind, PayloadJSON: payload})
			expectInterruptedResult(t, c.outbound, "orphan")
		})
	}
}
