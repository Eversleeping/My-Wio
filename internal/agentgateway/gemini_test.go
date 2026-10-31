package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

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
	activityPayload, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID, "phase": "model"})
	activityRaw, _ := json.Marshal(protocol.StreamEvent{EventID: "gemini-activity", StreamID: thread.ID, Kind: "gemini.activity", Payload: activityPayload})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: activityRaw}); err != nil {
		t.Fatal(err)
	}
	modePayload, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID, "approval_mode": "plan"})
	modeRaw, _ := json.Marshal(protocol.StreamEvent{EventID: "gemini-mode", StreamID: thread.ID, Kind: "gemini.mode", Payload: modePayload})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: modeRaw}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if current.ApprovalMode != "plan" {
		t.Fatalf("current mode update lost: %+v", current)
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
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: modeRaw}); err != nil {
		t.Fatal(err)
	}
	newThread, _ = db.GeminiThread(ctx, thread.ID)
	if newThread.ApprovalMode != "default" {
		t.Fatalf("stale mode changed later turn: %+v", newThread)
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

func TestGeminiLostOwnerUpdatesBrowserButKeepsWorkspaceReserved(t *testing.T) {
	for _, trigger := range []string{"reconciliation", "legacy-interrupt"} {
		t.Run(trigger, func(t *testing.T) {
			db, server := gatewayTestServer(t, "lost-"+trigger)
			ctx := context.Background()
			if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/lost", Name: "lost"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, _ := db.ListWorkspaces(ctx)
			thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.QueueGeminiTurn(ctx, thread, "work", "auto", "default"); err != nil {
				t.Fatal(err)
			}
			thread, _ = db.GeminiThread(ctx, thread.ID)
			if err := db.MarkRunning(ctx, thread.OperationID); err != nil {
				t.Fatal(err)
			}
			hub := realtime.New()
			_, updates := hub.Subscribe()
			g := New(db, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if trigger == "reconciliation" {
				payload, _ := json.Marshal(protocol.GeminiOwnership{OperationID: thread.OperationID})
				if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_ownership_lost", PayloadJSON: payload}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.InterruptGeminiTurn(ctx, thread); err != nil {
					t.Fatal(err)
				}
				pending, _ := db.PendingOperations(ctx, server.ID)
				var interruptID string
				for _, op := range pending {
					if op.Kind == "gemini.turn.interrupt" {
						interruptID = op.ID
					}
				}
				if interruptID == "" {
					t.Fatal("interrupt not queued")
				}
				payload, _ := json.Marshal(protocol.OperationResult{OperationID: interruptID, Status: "failed", Message: protocol.GeminiOwnershipLostMessage})
				if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
					t.Fatal(err)
				}
			}
			updated, err := db.GeminiThread(ctx, thread.ID)
			if err != nil || updated.Status != "interrupted" || !updated.RecoveryPending {
				t.Fatalf("lost owner not reflected in thread: %+v %v", updated, err)
			}
			select {
			case event := <-updates:
				if event.Kind != "gemini.turn.ownership_lost" || event.StreamID != thread.ID {
					t.Fatalf("wrong realtime update: %+v", event)
				}
			default:
				t.Fatal("missing realtime update")
			}
			if _, err := db.QueueGeminiTurn(ctx, updated, "another", "auto", "default"); err == nil {
				t.Fatal("unverified process released the workspace")
			}
			if err := db.DeleteGeminiThread(ctx, thread.ID); err == nil {
				t.Fatal("unverified session was deleted")
			}
			// A failed retry must return to the recovery state instead of
			// making the session look active again.
			if err := db.InterruptGeminiTurn(ctx, updated); err != nil {
				t.Fatal(err)
			}
			pending, _ := db.PendingOperations(ctx, server.ID)
			var failedRetry string
			for _, op := range pending {
				if op.Kind == "gemini.turn.interrupt" {
					failedRetry = op.ID
				}
			}
			failure, _ := json.Marshal(protocol.OperationResult{OperationID: failedRetry, Status: "failed", Message: "stop check timed out"})
			if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: failure}); err != nil {
				t.Fatal(err)
			}
			updated, _ = db.GeminiThread(ctx, thread.ID)
			if updated.Status != "interrupted" || !updated.RecoveryPending {
				t.Fatalf("failed retry lost recovery state: %+v", updated)
			}
			// A later explicit confirmation completes the original operation and
			// releases the workspace, even after ownership was initially lost.
			if err := db.InterruptGeminiTurn(ctx, updated); err != nil {
				t.Fatal(err)
			}
			pending, _ = db.PendingOperations(ctx, server.ID)
			var retry string
			for _, op := range pending {
				if op.Kind == "gemini.turn.interrupt" {
					retry = op.ID
				}
			}
			data, _ := json.Marshal(protocol.GeminiInterruptResult{OperationID: thread.OperationID, Stopped: true})
			payload, _ := json.Marshal(protocol.OperationResult{OperationID: retry, Status: "succeeded", Data: data})
			if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
				t.Fatal(err)
			}
			updated, _ = db.GeminiThread(ctx, thread.ID)
			if updated.Status != "interrupted" || updated.RecoveryPending {
				t.Fatalf("confirmed stop not finalized: %+v", updated)
			}
			if _, err := db.QueueGeminiTurn(ctx, updated, "resume", "auto", "default"); err != nil {
				t.Fatalf("confirmed stop did not release workspace: %v", err)
			}
		})
	}
}

func TestGeminiLateEventsAndResultsReconcileLostOwnership(t *testing.T) {
	db, server := gatewayTestServer(t, "lost-late")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/lost-late", Name: "late"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, _ := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if _, err := db.QueueGeminiTurn(ctx, thread, "work", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	_ = db.MarkRunning(ctx, thread.OperationID)
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := g.markGeminiOwnershipLost(ctx, server.ID, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	check, _ := json.Marshal(protocol.GeminiOwnership{OperationID: thread.OperationID})
	if err := g.handle(ctx, "foreign-server", &protocol.AgentEnvelope{Kind: "gemini_ownership_active", PayloadJSON: check}); err == nil {
		t.Fatal("foreign server claimed Gemini ownership")
	}
	if err := g.handle(ctx, "foreign-server", &protocol.AgentEnvelope{Kind: "gemini_ownership_lost", PayloadJSON: check}); err == nil {
		t.Fatal("foreign server reported lost Gemini ownership")
	}
	for _, malformed := range []protocol.StreamEvent{
		{EventID: "unsupported", StreamID: thread.ID, Kind: "gemini.unsupported", Payload: check},
		{EventID: "bad-init", StreamID: thread.ID, Kind: "gemini.init", Payload: check},
	} {
		raw, _ := json.Marshal(malformed)
		if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: raw}); err == nil {
			t.Fatal("invalid event was accepted")
		}
	}
	message, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID, "role": "assistant", "content": "still working"})
	event, _ := json.Marshal(protocol.StreamEvent{EventID: "late-message", StreamID: thread.ID, Kind: "gemini.message", Payload: message})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: event}); err != nil {
		t.Fatal(err)
	}
	updated, _ := db.GeminiThread(ctx, thread.ID)
	if updated.Status != "interrupted" || !updated.RecoveryPending {
		t.Fatalf("buffered output changed recovery state: %+v", updated)
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_ownership_active", PayloadJSON: check}); err != nil {
		t.Fatal(err)
	}
	updated, _ = db.GeminiThread(ctx, thread.ID)
	if updated.Status != "running" || updated.RecoveryPending {
		t.Fatalf("active owner did not resume tracking: %+v", updated)
	}
	if _, err := db.ConfirmGeminiStopped(ctx, thread.ID, thread.OperationID); err == nil {
		t.Fatal("stale confirmation released an active execution")
	}
	result, _ := json.Marshal(protocol.OperationResult{OperationID: thread.OperationID, Status: "interrupted", Message: "context canceled"})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
		t.Fatal(err)
	}
	updated, _ = db.GeminiThread(ctx, thread.ID)
	if updated.Status != "interrupted" || updated.RecoveryPending {
		t.Fatalf("interrupted receipt was not finalized: %+v", updated)
	}
	original, err := db.Operation(ctx, thread.OperationID)
	if err != nil || original.Status != "cancelled" {
		t.Fatalf("interrupted operation not recorded as cancelled: %+v %v", original, err)
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_event", PayloadJSON: event}); err != nil {
		t.Fatal(err)
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "gemini_ownership_active", PayloadJSON: check}); err != nil {
		t.Fatal(err)
	}
	updated, _ = db.GeminiThread(ctx, thread.ID)
	if updated.Status != "interrupted" {
		t.Fatal("late event resurrected a completed turn")
	}
}

func TestGeminiLostDeliveryIsProbedInsteadOfReexecuted(t *testing.T) {
	db, server := gatewayTestServer(t, "gemini-delivery")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/delivery", Name: "delivery"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, _ := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if _, err := db.QueueGeminiTurn(ctx, thread, "work", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	stream := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 2), onSend: func(message *protocol.ControlEnvelope) error {
		operation, err := db.Operation(ctx, message.OperationID)
		if err != nil || operation.Status != "delivered" {
			t.Fatalf("turn was sent before claiming delivery: %+v %v", operation, err)
		}
		return errors.New("connection lost during send")
	}}
	if err := g.flush(stream, server.ID); err == nil {
		t.Fatal("send failure not propagated")
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET delivered_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), thread.OperationID); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingOperations(ctx, server.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("possibly executed turn was redelivered: %+v %v", pending, err)
	}
	stream.onSend = nil
	if err := g.sendKeepalive(stream, server.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	message := <-stream.sent
	var check protocol.GeminiKeepalive
	if json.Unmarshal(message.PayloadJSON, &check) != nil || len(check.OperationIDs) != 1 || check.OperationIDs[0] != thread.OperationID {
		t.Fatalf("lost delivery was not probed: %+v", check)
	}
}

func TestGeminiLostOwnershipStateAndNotificationCommitTogether(t *testing.T) {
	db, server := gatewayTestServer(t, "lost-notification")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/lost-notification", Name: "lost"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, _ := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if _, err := db.QueueGeminiTurn(ctx, thread, "work", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	if err := db.MarkRunning(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, `CREATE TRIGGER fail_gemini_notice BEFORE INSERT ON events WHEN NEW.kind='gemini.turn.ownership_lost' BEGIN SELECT RAISE(ABORT, 'notice failed'); END`); err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := g.markGeminiOwnershipLost(ctx, server.ID, thread.OperationID); err == nil {
		t.Fatal("notification failure was ignored")
	}
	current, _ := db.GeminiThread(ctx, thread.ID)
	if current.Status != "queued" || current.RecoveryPending {
		t.Fatalf("state changed without notification: %+v", current)
	}
	if _, err := db.DB.ExecContext(ctx, "DROP TRIGGER fail_gemini_notice"); err != nil {
		t.Fatal(err)
	}
	if err := g.markGeminiOwnershipLost(ctx, server.ID, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if current.Status != "interrupted" || !current.RecoveryPending {
		t.Fatalf("repeated ownership check did not recover: %+v", current)
	}
}
