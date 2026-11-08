package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestReconcileOperationsAnswersFromExecutionsReceiptsOrUnknown(t *testing.T) {
	client := &Client{
		config:   Config{StateDir: t.TempDir()},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, 8),
		seen:     make(map[string]*operationExecution),
	}
	// A deploy can legitimately run for an hour; it must be reported active.
	client.seen["active"] = &operationExecution{kind: "deploy.start", done: make(chan struct{})}
	completed := &operationExecution{kind: "git.workspace.write", done: make(chan struct{}), result: protocol.OperationResult{OperationID: "completed", Status: "succeeded"}}
	close(completed.done)
	client.seen["completed"] = completed
	if err := client.saveOperationResult(protocol.OperationResult{OperationID: "receipt", Status: "failed", Message: "disk receipt"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.GeminiKeepalive{ReconcileOperationIDs: []string{"active", "completed", "receipt", "lost"}})
	client.reconcileOperations(raw)

	results := map[string]protocol.OperationResult{}
	states := map[string]string{}
	for range 4 {
		envelope := receiveAgentEnvelope(t, client.outbound)
		switch envelope.Kind {
		case "operation_result":
			var result protocol.OperationResult
			if err := json.Unmarshal(envelope.PayloadJSON, &result); err != nil {
				t.Fatal(err)
			}
			results[result.OperationID] = result
		case protocol.AgentKindOperationReconcile:
			var answer protocol.OperationReconcile
			if err := json.Unmarshal(envelope.PayloadJSON, &answer); err != nil {
				t.Fatal(err)
			}
			states[answer.OperationID] = answer.State
		default:
			t.Fatalf("unexpected reconcile message %q", envelope.Kind)
		}
	}
	if states["active"] != protocol.OperationReconcileActive || states["lost"] != protocol.OperationReconcileUnknown || len(states) != 2 {
		t.Fatalf("unexpected reconcile states: %#v", states)
	}
	if results["completed"].Status != "succeeded" || results["receipt"].Message != "disk receipt" || len(results) != 2 {
		t.Fatalf("stored results were not replayed: %#v", results)
	}
	// The probe never starts an operation.
	if len(client.seen) != 2 {
		t.Fatalf("reconcile registered executions: %#v", client.seen)
	}
}

func TestReconcileOperationsIgnoresKeepalivesWithoutRequest(t *testing.T) {
	client := &Client{outbound: make(chan *protocol.AgentEnvelope, 1), seen: make(map[string]*operationExecution)}
	raw, _ := json.Marshal(protocol.GeminiKeepalive{OperationIDs: []string{"gemini"}})
	client.reconcileOperations(raw)
	client.reconcileOperations(nil)
	if len(client.outbound) != 0 {
		t.Fatal("legacy keepalive produced reconcile answers")
	}
}

func TestRedeliveryOfRunningOperationRepeatsStartNotice(t *testing.T) {
	client := &Client{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, 4),
		seen:     make(map[string]*operationExecution),
	}
	execution := &operationExecution{kind: "deploy.start", done: make(chan struct{}), result: protocol.OperationResult{OperationID: "deploy", Status: "succeeded"}}
	client.seen["deploy"] = execution
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "deploy", Kind: "deploy.start"})
	}()
	receiveOperationStarted(t, client.outbound, "deploy")
	close(execution.done)
	<-finished
	if envelope := receiveAgentEnvelope(t, client.outbound); envelope.Kind != "operation_result" {
		t.Fatalf("redelivery did not replay the result: %q", envelope.Kind)
	}
}
