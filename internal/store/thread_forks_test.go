package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestMessageForkCopiesOnlySelectedHistory(t *testing.T) {
	assertMessageForkCopiesOnlySelectedHistory(t, testStore(t))
}

func assertMessageForkCopiesOnlySelectedHistory(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "message-fork", "message-fork")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/message-fork", Name: "message-fork"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var workspace Workspace
	for _, item := range workspaces {
		if item.ServerID == server.ID {
			workspace = item
		}
	}
	source, err := db.CreateThread(ctx, workspace.ID, "Original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "source-native", source.ID); err != nil {
		t.Fatal(err)
	}
	add := func(kind, payload string) protocol.StreamEvent {
		t.Helper()
		event, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: source.ID, Kind: kind, Payload: json.RawMessage(payload)})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	user := add("user.message", `{"text":"question"}`)
	commentary := add("codex.item.completed", `{"turnId":"keep","item":{"id":"progress","type":"agentMessage","phase":"commentary","text":"working"}}`)
	reply := add("codex.item.completed", `{"turnId":"keep","item":{"id":"answer","type":"agentMessage","text":"selected answer","images":["https://example.com/image.png"]}}`)
	if _, _, err := db.ThreadForkBoundary(ctx, source.ID, reply.EventID); !errors.Is(err, ErrInvalidForkTarget) {
		t.Fatalf("unfinished turn allowed: %v", err)
	}
	end := add("codex.turn.completed", `{"turn":{"id":"keep","status":"completed"}}`)
	add("user.message", `{"text":"must not leak"}`)
	add("codex.item.completed", `{"turnId":"later","item":{"id":"later-answer","type":"agentMessage","text":"future answer"}}`)
	add("codex.turn.completed", `{"turn":{"id":"later","status":"completed"}}`)
	for _, id := range []string{user.EventID, commentary.EventID, "missing"} {
		if _, _, err := db.ThreadForkBoundary(ctx, source.ID, id); !errors.Is(err, ErrInvalidForkTarget) {
			t.Fatalf("invalid target %s accepted: %v", id, err)
		}
	}
	if _, _, err := db.ThreadForkBoundary(ctx, "another-thread", reply.EventID); !errors.Is(err, ErrInvalidForkTarget) {
		t.Fatal("cross-thread target accepted", err)
	}
	turn, cutoff, err := db.ThreadForkBoundary(ctx, source.ID, reply.EventID)
	if err != nil || turn != "keep" || cutoff != end.Sequence {
		t.Fatalf("unexpected boundary %s %d %v", turn, cutoff, err)
	}
	command := protocol.ForkThreadCommand{SourceThreadID: source.ID, TargetThreadID: NewID(), CodexThread: "source-native", WorkspaceID: workspace.ID, Title: "Branch", LastTurnID: turn, CutoffSequence: cutoff}
	for range 2 {
		if err := db.CommitThreadFork(ctx, command, "new-native"); err != nil {
			t.Fatal(err)
		}
	}
	branch, err := db.Thread(ctx, command.TargetThreadID)
	if err != nil || branch.Status != "idle" {
		t.Fatalf("unexpected branch %#v %v", branch, err)
	}
	copy, err := db.Events(ctx, branch.ID, 0, 100)
	if err != nil || len(copy) != 4 {
		t.Fatalf("bad copy %#v %v", copy, err)
	}
	for _, event := range copy {
		if event.Sequence > cutoff || event.EventID == reply.EventID {
			t.Fatal("copied future history or reused event IDs")
		}
	}
	next, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: branch.ID, Kind: "user.message", Payload: json.RawMessage(`{"text":"continue branch"}`)})
	if err != nil || next.Sequence != cutoff+1 {
		t.Fatalf("bad branch sequence %#v %v", next, err)
	}
	original, err := db.Events(ctx, source.ID, 0, 100)
	if err != nil || len(original) != 7 {
		t.Fatalf("changed source: %d %v", len(original), err)
	}
	if err := ValidateThreadForkResult(command, protocol.ForkThreadResult{CodexThread: "new-native"}); err == nil {
		t.Fatal("old Agent result accepted")
	}
	if err := ValidateThreadForkResult(command, protocol.ForkThreadResult{CodexThread: "source-native", LastTurnID: turn}); err == nil {
		t.Fatal("source thread accepted as fork")
	}
	// A queued fork must not copy rewritten UI events into an older native context.
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "rewritten-native", source.ID); err != nil {
		t.Fatal(err)
	}
	command.TargetThreadID = NewID()
	if err := db.CommitThreadFork(ctx, command, "another-native"); err == nil {
		t.Fatal("fork accepted a rewritten source")
	}
	if _, err := db.Thread(ctx, command.TargetThreadID); err == nil {
		t.Fatal("failed fork created a visible chat")
	}
}
