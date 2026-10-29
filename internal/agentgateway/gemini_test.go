package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiEventsAreScopedAndTurnsComplete(t *testing.T) {
	db, server := gatewayTestServer(t, "gemini")
	ctx := context.Background()
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.79", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/gemini", Name: "gemini"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueGeminiTurn(ctx, thread, "first", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	eventPayload, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID, "session_id": "native-session"})
	raw, _ := json.Marshal(protocol.StreamEvent{EventID: "gemini-init", StreamID: thread.ID, Kind: "gemini.init", Payload: eventPayload})
	if err := g.handle(ctx, "foreign-server", &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: raw}); err == nil {
		t.Fatal("foreign Gemini event accepted")
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: raw}); err != nil {
		t.Fatal(err)
	}
	current, _ := db.GeminiThread(ctx, thread.ID)
	if current.SessionID != "native-session" || current.Status != "running" {
		t.Fatalf("session binding: %+v", current)
	}
	// A question uses its stable request id across the gateway and survives
	// credential redaction. Answer completion must not release the waiting turn.
	questionPayload, _ := json.Marshal(map[string]any{"operation_id": thread.OperationID, "questions": []any{map[string]any{"id": "choice", "question": "Which?", "isOther": true}}})
	questionRaw, _ := json.Marshal(protocol.StreamEvent{EventID: "question-id", StreamID: thread.ID, Kind: "gemini.question", Payload: questionPayload})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: questionRaw}); err != nil {
		t.Fatal(err)
	}
	questionEvents, _ := db.Events(ctx, thread.ID, 0, 500)
	if last := questionEvents[len(questionEvents)-1]; last.EventID != "question-id" || strings.Contains(string(last.Payload), "[REDACTED]") {
		t.Fatalf("question corrupted: %+v", last)
	}
	answerID, err := db.QueueGeminiAnswer(ctx, thread.ID, "question-id", map[string]protocol.UserInputAnswer{"choice": {Answers: []string{"Blue"}}})
	if err != nil {
		t.Fatal(err)
	}
	answerResult, _ := json.Marshal(protocol.OperationResult{OperationID: answerID, Status: "succeeded"})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: answerResult}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if current.OperationID != thread.OperationID || current.Status != "running" {
		t.Fatal("answer completed original turn")
	}
	readyPayload, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID})
	readyRaw, _ := json.Marshal(protocol.StreamEvent{EventID: thread.OperationID + ":steer-ready", StreamID: thread.ID, Kind: "gemini.steer.ready", Payload: readyPayload})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: readyRaw}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"succeeded", "failed"} {
		_, steerID, err := db.QueueGeminiSteer(ctx, thread.ID, thread.OperationID, "steer-"+status, "Focus on tests")
		if err != nil {
			t.Fatal(err)
		}
		steerResult, _ := json.Marshal(protocol.OperationResult{OperationID: steerID, Status: status, Message: "delivery result"})
		if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: steerResult}); err != nil {
			t.Fatal(err)
		}
		current, _ = db.GeminiThread(ctx, thread.ID)
		if current.OperationID != thread.OperationID || current.Status != "running" {
			t.Fatalf("steer %s completed original turn", status)
		}
	}
	steerEvents, _ := db.Events(ctx, thread.ID, 0, 500)
	last := steerEvents[len(steerEvents)-1]
	if last.Kind != "gemini.steer.rejected" || !strings.Contains(string(last.Payload), "steer-failed") {
		t.Fatalf("missing delivery failure: %+v", last)
	}
	if err := db.MarkDelivered(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := db.InterruptGeminiTurn(ctx, current); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(protocol.OperationResult{OperationID: thread.OperationID, Status: "failed", Message: "context canceled"})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if current.Status != "interrupted" {
		t.Fatalf("completion: %+v", current)
	}
	// Finish the queued interrupt before starting a later task.
	operations, _ := db.PendingOperations(ctx, server.ID)
	for _, op := range operations {
		if op.Kind == "gemini.turn.interrupt" {
			_ = db.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"})
		}
	}
	if _, err := db.QueueGeminiTurn(ctx, current, "second", "flash", "default"); err != nil {
		t.Fatal(err)
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: raw}); err != nil {
		t.Fatal(err)
	}
	newThread, _ := db.GeminiThread(ctx, thread.ID)
	if newThread.Status != "queued" {
		t.Fatalf("stale init changed later turn: %+v", newThread)
	}
	op, _ := db.Operation(ctx, newThread.OperationID)
	var command protocol.GeminiTurnCommand
	_ = json.Unmarshal([]byte(op.Payload), &command)
	if command.SessionID != "native-session" || command.Model != "flash" {
		t.Fatalf("resume: %+v", command)
	}
	// The workspace reservation rolls back the other session's claim and event.
	other, _ := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if _, err := db.QueueGeminiTurn(ctx, other, "conflict", "pro", "auto_edit"); err == nil {
		t.Fatal("concurrent workspace write accepted")
	}
	other, _ = db.GeminiThread(ctx, other.ID)
	events, _ := db.Events(ctx, other.ID, 0, 500)
	if other.Status != "idle" || len(events) != 0 {
		t.Fatalf("claim was not rolled back: %+v %v", other, events)
	}
	plan, err := db.WorkspaceDeletionPlan(ctx, workspaces[0].ID, false)
	if err != nil || plan.ThreadCount != 2 || plan.CanRemoveRecord {
		t.Fatalf("Gemini sessions missing from workspace deletion plan: %+v %v", plan, err)
	}
}

func TestGeminiInterruptOnlyReleasesWorkspaceAfterConfirmedStop(t *testing.T) {
	for _, test := range []struct {
		name, resultStatus, target, wantStatus string
		stopped                                bool
	}{
		{"legacy-ack", "succeeded", "", "interrupting", false},
		{"wrong-target", "succeeded", "another-operation", "interrupting", true},
		{"missing-owner", "failed", "", "running", false},
		{"confirmed-exit", "succeeded", "current", "interrupted", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, server := gatewayTestServer(t, "stop-"+test.name)
			ctx := context.Background()
			if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.80", GeminiVersion: "0.66.0"}); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/gemini", Name: "gemini"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, _ := db.ListWorkspaces(ctx)
			thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "yolo")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.QueueGeminiTurn(ctx, thread, "first", "auto", "yolo"); err != nil {
				t.Fatal(err)
			}
			thread, _ = db.GeminiThread(ctx, thread.ID)
			if err := db.MarkDelivered(ctx, thread.OperationID); err != nil {
				t.Fatal(err)
			}
			if err := db.InterruptGeminiTurn(ctx, thread); err != nil {
				t.Fatal(err)
			}
			operations, _ := db.PendingOperations(ctx, server.ID)
			var interrupt store.Operation
			for _, operation := range operations {
				if operation.Kind == "gemini.turn.interrupt" {
					interrupt = operation
				}
			}
			if interrupt.ID == "" {
				t.Fatal("interrupt was not queued")
			}
			g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			target := test.target
			if target == "current" {
				target = thread.OperationID
			}
			confirmation, _ := json.Marshal(protocol.GeminiInterruptResult{OperationID: target, Stopped: test.stopped})
			raw, _ := json.Marshal(protocol.OperationResult{OperationID: interrupt.ID, Status: test.resultStatus, Data: confirmation, Message: "stop result"})
			if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: raw}); err != nil {
				t.Fatal(err)
			}
			current, _ := db.GeminiThread(ctx, thread.ID)
			if current.Status != test.wantStatus {
				t.Fatalf("status=%s, want %s", current.Status, test.wantStatus)
			}
			other, _ := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
			_, queueErr := db.QueueGeminiTurn(ctx, other, "second", "auto", "default")
			if test.stopped && target == thread.OperationID {
				if queueErr != nil {
					t.Fatalf("confirmed exit did not release workspace: %v", queueErr)
				}
				original, _ := db.Operation(ctx, thread.OperationID)
				if original.Status != "cancelled" {
					t.Fatalf("original operation left active: %+v", original)
				}
				// A delayed original result must not undo interrupted status.
				late, _ := json.Marshal(protocol.OperationResult{OperationID: thread.OperationID, Status: "failed", Message: "context canceled"})
				if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: late}); err != nil {
					t.Fatal(err)
				}
				current, _ = db.GeminiThread(ctx, thread.ID)
				if current.Status != "interrupted" {
					t.Fatal("late result changed completed interruption")
				}
			} else if queueErr == nil {
				t.Fatal("unverified stop released workspace")
			}
		})
	}
}
