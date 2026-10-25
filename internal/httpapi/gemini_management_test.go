package httpapi

import (
	"context"
	"encoding/json"
	"github.com/wio-platform/wio/internal/protocol"
	"testing"
)

func TestGeminiForkCopiesOnlyCompletedPrefixAndDoesNotChangeSource(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "gemini-fork")
	db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.67"})
	db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/fork", Name: "fork"}}})
	workspaces, _ := db.ListWorkspaces(ctx)
	source, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET session_id='native-original' WHERE id=?"), source.ID)
	add := func(kind string, p map[string]any) protocol.StreamEvent {
		raw, _ := json.Marshal(p)
		e, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: source.ID, Kind: kind, Payload: raw})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	add("gemini.user.message", map[string]any{"text": "kept", "operation_id": "op1"})
	add("gemini.init", map[string]any{"timestamp": "2026-10-25T00:00:00Z", "operation_id": "op1", "session_id": "native-original"})
	reply := add("gemini.message", map[string]any{"role": "assistant", "content": "answer1", "operation_id": "op1"})
	result := add("gemini.result", map[string]any{"status": "success", "operation_id": "op1"})
	add("gemini.user.message", map[string]any{"text": "future", "operation_id": "op2"})
	add("gemini.init", map[string]any{"timestamp": "2026-10-25T00:01:00Z", "operation_id": "op2", "session_id": "native-original"})
	add("gemini.message", map[string]any{"role": "assistant", "content": "answer2", "operation_id": "op2"})
	add("gemini.result", map[string]any{"status": "success", "operation_id": "op2"})
	source, _ = db.GeminiThread(ctx, source.ID)
	if !source.ForkSupported {
		t.Fatal("Agent capability missing")
	}
	if _, _, err = db.QueueGeminiFork(ctx, source, "missing", nil); err == nil {
		t.Fatal("invalid boundary accepted")
	}
	op, target, err := db.QueueGeminiFork(ctx, source, reply.EventID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.QueueGeminiTurn(ctx, source, "racing", "flash", "default"); err == nil {
		t.Fatal("send raced snapshot")
	}
	operation, _ := db.Operation(ctx, op)
	var command protocol.GeminiForkCommand
	json.Unmarshal([]byte(operation.Payload), &command)
	if command.Before == nil || command.Before.StartedAt != "2026-10-25T00:01:00Z" || command.CutoffSequence != result.Sequence {
		t.Fatalf("boundary: %+v", command)
	}
	if err = db.CommitGeminiFork(ctx, command, "native-original"); err == nil {
		t.Fatal("shared native context accepted")
	}
	if err = db.CommitGeminiFork(ctx, command, "native-branch"); err != nil {
		t.Fatal(err)
	}
	if err = db.CommitGeminiFork(ctx, command, "native-branch"); err != nil {
		t.Fatal(err)
	}
	events, _ := db.Events(ctx, target, 0, 500)
	original, _ := db.Events(ctx, source.ID, 0, 500)
	if len(events) != 4 || len(original) != 8 || events[0].EventID == original[0].EventID {
		t.Fatal("branch copied future or changed source")
	}
	branch, _ := db.GeminiThread(ctx, target)
	if branch.SessionID != "native-branch" || branch.WorkspaceID != source.WorkspaceID {
		t.Fatal("branch lost native ID/workspace")
	}
	db.CompleteGeminiTurn(ctx, source.ID, op, "idle")
	if err = db.PinGeminiThread(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	branch, _ = db.GeminiThread(ctx, target)
	if branch.PinnedAt == nil {
		t.Fatal("pin not persisted")
	}
	n, err := db.ArchiveGeminiProject(ctx, source.ProjectID)
	if err != nil || n != 2 {
		t.Fatalf("archive project: %d %v", n, err)
	}
}
