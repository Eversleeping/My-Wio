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

func TestClaudeQuestionReceiptsPreserveRunningParent(t *testing.T) {
	db, server := gatewayTestServer(t, "claude-questions")
	ctx := context.Background()
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.98", ClaudeVersion: "2.1.318"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/questions", Name: "questions"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.QueueClaudeTurn(ctx, thread, "Ask me", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if _, err = db.ClaimGeminiDelivery(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	send := func(serverID, kind, id string, payload any) error {
		raw, _ := json.Marshal(payload)
		event, _ := json.Marshal(protocol.StreamEvent{EventID: id, StreamID: thread.ID, Kind: kind, Payload: raw})
		return g.handle(ctx, serverID, &protocol.AgentEnvelope{Kind: "claude_event", PayloadJSON: event})
	}
	payload := map[string]any{"operation_id": thread.OperationID, "request_id": "question", "questions": []any{map[string]any{"id": "0", "question": "Which?", "multiSelect": true, "isOther": true}}}
	if err := send("foreign", "claude.question", "question", payload); err == nil {
		t.Fatal("accepted foreign question")
	}
	if err := send(server.ID, "claude.question", "question", payload); err != nil {
		t.Fatal(err)
	}
	answers := map[string]protocol.UserInputAnswer{"0": {Answers: []string{"A", "B"}}}
	id, err := db.QueueClaudeAnswer(ctx, thread.ID, "question", answers)
	if err != nil {
		t.Fatal("redaction broke questions", err)
	}
	if err := send(server.ID, "claude.question.answered", "question:question.answered", map[string]any{"operation_id": thread.OperationID, "request_id": "question", "answers": answers}); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(protocol.OperationResult{OperationID: id, Status: "succeeded"})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
		t.Fatal(err)
	}
	current, _ := db.ClaudeThread(ctx, thread.ID)
	parent, _ := db.Operation(ctx, thread.OperationID)
	if current.Status != "running" || parent.Status != "delivered" {
		t.Fatal("answer completed parent", current.Status, parent.Status)
	}
	events, _ := db.Events(ctx, thread.ID, 0, 500)
	found := false
	for _, event := range events {
		if event.Kind == "claude.question.answered" {
			found = true
		}
	}
	if !found {
		t.Fatal("answer receipt not persisted")
	}
}
