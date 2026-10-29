package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiSteerScopesDeliveryRetriesAndNextTurnContext(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "gemini-steer-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.79", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/steering", Name: "steering"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	thread, err = db.GeminiThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !thread.SteerSupported {
		t.Fatal("compatible Agent/CLI missing steering capability")
	}
	if _, err = db.QueueGeminiTurn(ctx, thread, "Start", "flash", "default"); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	op := thread.OperationID
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	requestID := store.NewID()
	send := func(operation, id, prompt string) int {
		response := threadResourceRequest(t, http.MethodPost, "/steer", thread.ID, map[string]any{"prompt": prompt, "operation_id": operation, "request_id": id}, api.steerGeminiTurn)
		return response.Code
	}
	if code := send(op, requestID, "Focus on tests"); code != 409 {
		t.Fatalf("unready accepted: %d", code)
	}
	add := func(kind string, payload map[string]any) {
		payload["operation_id"] = op
		raw, _ := json.Marshal(payload)
		_, err := db.AddEvent(ctx, protocol.StreamEvent{EventID: op + ":" + kind, StreamID: thread.ID, Kind: kind, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("gemini.init", map[string]any{"session_id": "native"})
	add("gemini.steer.ready", map[string]any{})
	// Ready state survives loading a page whose event window excludes startup.
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE events SET event_id=? WHERE stream_id=? AND kind='gemini.steer.ready'"), op+":steer-ready", thread.ID); err != nil {
		t.Fatal(err)
	}
	if code := send("stale", requestID, "Focus on tests"); code != 409 {
		t.Fatalf("stale turn accepted: %d", code)
	}
	if code := send(op, requestID, "Focus on tests"); code != 202 {
		t.Fatalf("steer: %d", code)
	}
	if code := send(op, requestID, "Focus on tests"); code != 202 {
		t.Fatalf("identical retry: %d", code)
	}
	if code := send(op, requestID, "Different"); code != 409 {
		t.Fatalf("changed retry: %d", code)
	}
	current, _ := db.GeminiThread(ctx, thread.ID)
	if current.Status != "running" || current.OperationID != op || !current.SteerReady {
		t.Fatal("steering changed lifecycle or readiness lost")
	}
	operations, err := db.PendingOperations(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	steers := 0
	for _, item := range operations {
		if item.Kind == "gemini.turn.steer" {
			steers++
			if item.WorkspaceWrite != 0 {
				t.Fatal("steer reserved the running workspace")
			}
		}
	}
	if steers != 1 {
		t.Fatalf("retry duplicated operation: %d", steers)
	}
	add("gemini.steer.applied", map[string]any{"request_id": requestID})
	add("gemini.result", map[string]any{"status": "success"})
	if code := send(op, store.NewID(), "Late"); code != 409 {
		t.Fatalf("completed model request accepted steer: %d", code)
	}
	if err = db.CompleteGeminiTurn(ctx, thread.ID, op, "idle"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET status='succeeded' WHERE id=?"), op); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if _, err = db.QueueGeminiTurn(ctx, current, "Continue", "flash", "default"); err != nil {
		t.Fatal(err)
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	next, _ := db.Operation(ctx, current.OperationID)
	var command protocol.GeminiTurnCommand
	_ = json.Unmarshal([]byte(next.Payload), &command)
	if !command.Steering || len(command.SteeringContext) != 1 || command.SteeringContext[0] != "Focus on tests" {
		t.Fatalf("applied context not retained: %+v", command)
	}
	if code := send(op, store.NewID(), "Old turn"); code != 409 {
		t.Fatalf("new turn accepted stale steer: %d", code)
	}
}
