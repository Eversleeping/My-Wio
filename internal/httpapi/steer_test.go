package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestSteerQueuesAgainstCapturedTurnWithoutChangingStatus(t *testing.T) {
	ctx := context.Background()
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "steer-token")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.51"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/steer", Name: "steer"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "steer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET codex_thread_id=?,status='running' WHERE id=?"), "native-thread", thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.started", Payload: json.RawMessage(`{"turn":{"id":"active-turn"}}`)}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	path := "/api/threads/" + thread.ID + "/steer"
	image := "data:image/png;base64,iVBORw0KGgo="
	response := threadResourceRequest(t, http.MethodPost, path, thread.ID, map[string]any{"prompt": "  Focus on tests  ", "images": []map[string]string{{"data_url": image}}}, api.steerTurn)
	if response.Code != http.StatusAccepted {
		t.Fatalf("steer: %d %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.turn.steer" {
		t.Fatalf("operations: %#v %v", operations, err)
	}
	var command protocol.SteerTurnCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.ThreadID != thread.ID || command.CodexThread != "native-thread" || command.ExpectedTurnID != "active-turn" || command.Prompt != "Focus on tests" || len(command.Images) != 1 {
		t.Fatalf("command: %#v", command)
	}
	current, err := database.Thread(ctx, thread.ID)
	if err != nil || current.Status != "running" {
		t.Fatalf("steer changed lifecycle: %#v %v", current, err)
	}
	events, err := database.RecentConversationEvents(ctx, thread.ID, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "user.message" || !strings.Contains(string(events[0].Payload), `"steer":true`) {
		t.Fatalf("correction missing: %#v %v", events, err)
	}
	if _, _, err := database.RewriteThread(ctx, current, events[0].EventID, protocol.StartTurnCommand{}, json.RawMessage(`{"text":"edit"}`)); err == nil {
		t.Fatal("active correction could be rewritten")
	}
	for _, status := range []string{"queued", "idle", "completed"} {
		if err := database.SetThreadStatus(ctx, thread.ID, status); err != nil {
			t.Fatal(err)
		}
		response := threadResourceRequest(t, http.MethodPost, path, thread.ID, map[string]string{"prompt": "late"}, api.steerTurn)
		if response.Code != http.StatusConflict {
			t.Fatalf("%s accepted steering: %d", status, response.Code)
		}
	}
	if err := database.SetThreadStatus(ctx, thread.ID, "running"); err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.50"}); err != nil {
		t.Fatal(err)
	}
	response = threadResourceRequest(t, http.MethodPost, path, thread.ID, map[string]string{"prompt": "old agent"}, api.steerTurn)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "steer_agent_update") {
		t.Fatalf("old agent: %d %s", response.Code, response.Body.String())
	}
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.51"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"id":"active-turn","status":"completed"}}`)}); err != nil {
		t.Fatal(err)
	}
	response = threadResourceRequest(t, http.MethodPost, path, thread.ID, map[string]string{"prompt": "race with completion"}, api.steerTurn)
	if response.Code != http.StatusConflict {
		t.Fatal("completed event accepted steering")
	}
	operations, err = database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 {
		t.Fatalf("rejected request created operation: %#v %v", operations, err)
	}
	if err := database.SetThreadStatus(ctx, thread.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	current, _ = database.Thread(ctx, thread.ID)
	if _, _, err := database.RewriteThread(ctx, current, events[0].EventID, protocol.StartTurnCommand{}, json.RawMessage(`{"text":"edit"}`)); err == nil {
		t.Fatal("steer message can be rewritten as an independent turn")
	}
}

func TestSteerRejectsEmptyImagesAndTurnOverrides(t *testing.T) {
	api := resourceTestAPI(openBootstrapTestStore(t))
	for _, input := range []map[string]any{
		{"prompt": " "},
		{"images": []map[string]string{{"data_url": "file:///private.png"}}},
		{"prompt": "correction", "model": "other-model"},
	} {
		response := threadResourceRequest(t, http.MethodPost, "/api/threads/missing/steer", "missing", input, api.steerTurn)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input: %d %s", response.Code, response.Body.String())
		}
	}
}
