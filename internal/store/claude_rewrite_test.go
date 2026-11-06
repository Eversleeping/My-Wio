package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeRewriteStorage(t *testing.T) { assertClaudeRewriteStorage(t, testStore(t)) }

func assertClaudeRewriteStorage(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-rewrite", "claude-rewrite")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-rewrite", Name: "rewrite"}}}); err != nil {
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
	const oldSession = "550e8400-e29b-41d4-a716-446655440000"
	const newSession = "550e8400-e29b-41d4-a716-446655440001"
	var target protocol.StreamEvent
	for _, prompt := range []string{"keep", "edit", "discard"} {
		thread, _ = db.ClaudeThread(ctx, thread.ID)
		u, err := db.QueueClaudeTurn(ctx, thread, prompt, "sonnet", "default", "")
		if err != nil {
			t.Fatal(err)
		}
		if prompt == "edit" {
			target = u
		}
		thread, _ = db.ClaudeThread(ctx, thread.ID)
		if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.message", Payload: json.RawMessage(`{"text":"` + prompt + ` answer"}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET status='succeeded' WHERE id=?"), thread.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='idle',session_id=? WHERE id=?"), oldSession, thread.ID); err != nil {
			t.Fatal(err)
		}
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	original, _ := db.Events(ctx, thread.ID, 0, 500)
	for _, id := range []string{"foreign", original[1].EventID} {
		if _, err := db.QueueClaudeRewrite(ctx, thread, id, "revised", "sonnet", "default", ""); err == nil {
			t.Fatal("invalid edit accepted", id)
		}
	}
	image := protocol.TurnImage{DataURL: "data:image/png;base64,dGVzdA=="}
	if _, err := db.QueueClaudeRewrite(ctx, thread, target.EventID, "revised", "sonnet", "default", "", image); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	unchanged, _ := db.Events(ctx, thread.ID, 0, 500)
	if len(unchanged) != len(original) {
		t.Fatal("queue removed history")
	}
	if _, _, err := db.CommitClaudeRewrite(ctx, thread.ID, thread.OperationID, oldSession); err == nil {
		t.Fatal("accepted source session")
	}
	event, changed, err := db.CommitClaudeRewrite(ctx, thread.ID, thread.OperationID, newSession)
	if err != nil || !changed {
		t.Fatalf("commit: %v %v", changed, err)
	}
	if event.Sequence <= original[len(original)-1].Sequence || !strings.Contains(string(event.Payload), image.DataURL) {
		t.Fatal("lost images or cursor", event)
	}
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.message", Payload: json.RawMessage(`{"text":"new answer"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := db.CommitClaudeRewrite(ctx, thread.ID, thread.OperationID, newSession); err != nil || changed {
		t.Fatalf("duplicate commit: %v %v", changed, err)
	}
	current, _ := db.Events(ctx, thread.ID, 0, 500)
	raw, _ := json.Marshal(current)
	for _, text := range []string{"keep answer", "revised", "new answer"} {
		if !strings.Contains(string(raw), text) {
			t.Fatal("missing retained content", text)
		}
	}
	if strings.Contains(string(raw), "discard") || strings.Contains(string(raw), "edit answer") {
		t.Fatalf("kept old tail: %s", raw)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET status='cancelled' WHERE id=?"), thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CommitClaudeRewrite(ctx, thread.ID, thread.OperationID, newSession); err == nil {
		t.Fatal("late init accepted after cancellation")
	}
}

func TestClaudeMessageForkRequiresLatestAssistant(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-fork-target", "claude-fork-target")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/fork-target", Name: "fork"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET session_id=? WHERE id=?"), "550e8400-e29b-41d4-a716-446655440000", thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	first, _ := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.message", Payload: json.RawMessage(`{"text":"earlier"}`)})
	u, _ := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.user.message", Payload: json.RawMessage(`{"text":"new"}`)})
	for _, id := range []string{first.EventID, u.EventID, "foreign"} {
		if _, _, err := db.QueueClaudeFork(ctx, thread, nil, id); err == nil {
			t.Fatal("invalid branch target accepted", id)
		}
	}
	last, _ := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.message", Payload: json.RawMessage(`{"text":"last"}`)})
	if _, _, err := db.QueueClaudeFork(ctx, thread, nil, first.EventID); err == nil {
		t.Fatal("branched earlier assistant")
	}
	if _, _, err := db.QueueClaudeFork(ctx, thread, nil, last.EventID); err != nil {
		t.Fatal("last reply branch", err)
	}
}
