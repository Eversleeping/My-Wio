package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
)

func TestGeminiEventsAreScopedAndTurnsComplete(t *testing.T) {
	db, server := gatewayTestServer(t, "gemini")
	ctx := context.Background()
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
