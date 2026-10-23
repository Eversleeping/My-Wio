package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestMessageForkQueuesExactBoundaryAndGuardsOldAgents(t *testing.T) {
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "message-fork")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/fork", Name: "fork"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := db.CreateThread(ctx, workspaces[0].ID, "Source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "source-native", thread.ID); err != nil {
		t.Fatal(err)
	}
	reply, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.item.completed", Payload: json.RawMessage(`{"turnId":"keep","item":{"id":"answer","type":"agentMessage","text":"answer"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	end, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"id":"keep","status":"completed"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	call := func(id string) int {
		t.Helper()
		response := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/fork", thread.ID, map[string]string{"event_id": id}, api.forkThread)
		return response.Code
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.58"}); err != nil {
		t.Fatal(err)
	}
	if status := call(reply.EventID); status != http.StatusConflict {
		t.Fatalf("old Agent accepted: %d", status)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.59"}); err != nil {
		t.Fatal(err)
	}
	if status := call("missing"); status != http.StatusBadRequest {
		t.Fatalf("invalid message accepted: %d", status)
	}
	if status := call(end.EventID); status != http.StatusBadRequest {
		t.Fatalf("non-message accepted: %d", status)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if status := call(reply.EventID); status != http.StatusConflict {
		t.Fatalf("active thread accepted: %d", status)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET status='idle' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if status := call(reply.EventID); status != http.StatusAccepted {
		t.Fatalf("fork rejected: %d", status)
	}
	ops, err := db.PendingOperations(ctx, server.ID)
	if err != nil || len(ops) != 1 {
		t.Fatalf("bad operations %#v %v", ops, err)
	}
	var command protocol.ForkThreadCommand
	if err := json.Unmarshal([]byte(ops[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.LastTurnID != "keep" || command.CutoffSequence != end.Sequence || command.CodexThread != "source-native" || command.TargetThreadID == thread.ID {
		t.Fatalf("bad command %#v", command)
	}
	if !strings.Contains(command.Title, "branch") {
		t.Fatal(command.Title)
	}
}
