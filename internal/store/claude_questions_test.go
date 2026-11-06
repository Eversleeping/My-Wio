package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeQuestionsStorage(t *testing.T) { assertClaudeQuestionsStorage(t, testStore(t)) }
func assertClaudeQuestionsStorage(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-question", "claude-question")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.98", ClaudeVersion: "2.1.318", ClaudeReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-question", Name: "question"}}}); err != nil {
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
	if !thread.QuestionsSupported {
		t.Fatal("missing capability")
	}
	if _, err = db.QueueClaudeTurn(ctx, thread, "Ask me", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	op, _ := db.Operation(ctx, thread.OperationID)
	var turn protocol.ClaudeTurnCommand
	if json.Unmarshal([]byte(op.Payload), &turn) != nil || !turn.Questions {
		t.Fatal("question protocol disabled")
	}
	if _, err = db.ClaimGeminiDelivery(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	question := func(id, operation string) {
		raw, _ := json.Marshal(map[string]any{"operation_id": operation, "questions": []any{map[string]any{"id": "0", "question": "Pick", "multiSelect": true, "isOther": true}}})
		if _, err := db.AddEvent(ctx, protocol.StreamEvent{EventID: id, StreamID: thread.ID, Kind: "claude.question", Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	question("question-id", thread.OperationID)
	answers := map[string]protocol.UserInputAnswer{"0": {Answers: []string{"A", "B"}}}
	id, err := db.QueueClaudeAnswer(ctx, thread.ID, "question-id", answers)
	if err != nil {
		t.Fatal(err)
	}
	answerOp, _ := db.Operation(ctx, id)
	if answerOp.Kind != "claude.question.answer" || answerOp.WorkspaceWrite != 0 || answerOp.WorkspaceID != workspace.ID {
		t.Fatal("answer acquired parent write lock", answerOp)
	}
	duplicate, err := db.QueueClaudeAnswer(ctx, thread.ID, "question-id", answers)
	if err != nil || duplicate != id {
		t.Fatal("idempotency", duplicate, err)
	}
	if _, err := db.QueueClaudeAnswer(ctx, thread.ID, "question-id", map[string]protocol.UserInputAnswer{"0": {Answers: []string{"Other"}}}); err == nil {
		t.Fatal("overwrote answer")
	}
	if _, err := db.QueueClaudeAnswer(ctx, thread.ID, "question-id", map[string]protocol.UserInputAnswer{}); err == nil {
		t.Fatal("accepted empty answers")
	}
	question("old-question", "old-op")
	if _, err := db.QueueClaudeAnswer(ctx, thread.ID, "old-question", answers); err == nil {
		t.Fatal("answered old task")
	}
	question("cancelled-question", thread.OperationID)
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{EventID: "cancelled-question:question.cancelled", StreamID: thread.ID, Kind: "claude.question.cancelled", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueClaudeAnswer(ctx, thread.ID, "cancelled-question", answers); err == nil {
		t.Fatal("answered cancelled question")
	}
	question("expired-question", thread.OperationID)
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='idle' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueClaudeAnswer(ctx, thread.ID, "expired-question", answers); err == nil {
		t.Fatal("answered ended task")
	}
	if duplicate, err := db.QueueClaudeAnswer(ctx, thread.ID, "question-id", answers); err != nil || duplicate != id {
		t.Fatal("lost receipt after completion", err)
	}
}
