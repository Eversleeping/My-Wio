package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeSteerStorage(t *testing.T) { assertClaudeSteerStorage(t, testStore(t)) }
func assertClaudeSteerStorage(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-steer", "claude-steer")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.95", ClaudeVersion: "2.1.318 (Claude Code)", ClaudeReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-steer", Name: "steer"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	var workspace Workspace
	for _, w := range workspaces {
		if w.ServerID == server.ID {
			workspace = w
		}
	}
	thread, err := db.CreateClaudeThread(ctx, workspace.ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if !thread.SteerSupported {
		t.Fatal("missing capability")
	}
	if _, err = db.QueueClaudeTurn(ctx, thread, "start", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	c := protocol.ClaudeSteerCommand{ThreadID: thread.ID, OperationID: thread.OperationID, RequestID: uuid.NewString(), Prompt: "change direction"}
	if _, _, err = db.QueueClaudeSteer(ctx, c); err == nil {
		t.Fatal("steered queued task")
	}
	if _, err = db.ClaimGeminiDelivery(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.QueueClaudeSteer(ctx, c); err == nil {
		t.Fatal("steered before ready")
	}
	raw, _ := json.Marshal(map[string]any{"operation_id": thread.OperationID, "ready": true})
	if _, err = db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.steer.ready", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	event, id, err := db.QueueClaudeSteer(ctx, c)
	if err != nil || event.Kind != "claude.steer.queued" {
		t.Fatal("queue", err, event)
	}
	op, _ := db.Operation(ctx, id)
	if op.WorkspaceWrite != 0 || op.Kind != "claude.turn.steer" || op.WorkspaceID != workspace.ID {
		t.Fatal("wrong resource lock", op)
	}
	var initial protocol.ClaudeTurnCommand
	parent, _ := db.Operation(ctx, thread.OperationID)
	if json.Unmarshal([]byte(parent.Payload), &initial) != nil || !initial.Steer {
		t.Fatal("native input not enabled")
	}
	duplicate, duplicateID, err := db.QueueClaudeSteer(ctx, c)
	if err != nil || duplicate.EventID != "" || duplicateID != id {
		t.Fatal("duplicate instruction", err, duplicateID)
	}
	changed := c
	changed.Prompt = "another instruction"
	if _, _, err = db.QueueClaudeSteer(ctx, changed); err == nil {
		t.Fatal("changed idempotent input")
	}
	changed = c
	changed.RequestID = uuid.NewString()
	changed.OperationID = uuid.NewString()
	if _, _, err = db.QueueClaudeSteer(ctx, changed); err == nil {
		t.Fatal("stale turn")
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if !thread.SteerReady {
		t.Fatal("ready state missing")
	}
	raw, _ = json.Marshal(map[string]any{"operation_id": thread.OperationID, "ready": false})
	if _, err = db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.steer.closed", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	changed = c
	changed.RequestID = uuid.NewString()
	if _, _, err = db.QueueClaudeSteer(ctx, changed); err == nil {
		t.Fatal("accepted after input closed")
	}
	if _, retryID, err := db.QueueClaudeSteer(ctx, c); err != nil || retryID != id {
		t.Fatal("lost safe retry after close", err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if thread.SteerReady {
		t.Fatal("closed state still ready")
	}
}
