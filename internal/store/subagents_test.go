package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/subagent"
)

func TestSubagentHistoryPersistsAndPaginatesWithinParent(t *testing.T) {
	assertSubagentHistory(t, testStore(t))
}

func assertSubagentHistory(t *testing.T, db *Store) {
	ctx := context.Background()
	f := newCollaborationFixture(t, db, "codex")
	ref := session.Ref{Engine: "codex", ID: f.source.ID}
	updates := []protocol.SubagentUpdate{
		{ID: "child", Name: "Review", Status: "running", Prompt: "Check code", Detail: "activity"},
		{ID: "child", Entry: &protocol.SubagentEntry{ID: "tool", Kind: "tool", Tool: "Read", Status: "running"}},
		{ID: "other", Name: "Another child", Status: "running"},
		{ID: "other", Entry: &protocol.SubagentEntry{ID: "private", Kind: "message", Text: "other output"}},
		{ID: "child", Entry: &protocol.SubagentEntry{ID: "tool", Kind: "tool", Status: "succeeded", Output: json.RawMessage(`"file contents"`)}},
		{ID: "child", Status: "succeeded"},
	}
	for _, update := range updates {
		if _, err := db.AddEvent(ctx, subagent.Event(ref.ID, update)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := db.ReadSubagents(ctx, ref)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	var child Subagent
	for _, item := range list {
		if item.ID == "child" {
			child = item
		}
	}
	if child.Name != "Review" || child.Status != "succeeded" || child.Prompt != "Check code" || child.Entry != nil {
		t.Fatalf("metadata lost: %+v", child)
	}
	first, err := db.SubagentEvents(ctx, ref.ID, "child", 0, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first page %v %v", first, err)
	}
	second, err := db.SubagentEvents(ctx, ref.ID, "child", first[0].Sequence, 10)
	if err != nil || len(second) != 1 || second[0].Sequence <= first[0].Sequence {
		t.Fatalf("second page %v %v", second, err)
	}
	wrong, err := db.SubagentEvents(ctx, "unrelated-session", "child", 0, 10)
	if err != nil || len(wrong) != 0 {
		t.Fatalf("cross-parent data: %v %v", wrong, err)
	}
	legacy := protocol.StreamEvent{StreamID: ref.ID, Kind: protocol.EventCodexItemCompleted, Payload: json.RawMessage(`{"item":{"type":"collabAgentToolCall","status":"completed","tool":"spawnAgent","receiverThreadIds":["child"],"agentsStates":{}}}`)}
	if _, err := db.AddEvent(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	list, err = db.ReadSubagents(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID == "child" && item.Status != "succeeded" {
			t.Fatalf("coordination overwrote child status: %+v", item)
		}
	}
}
