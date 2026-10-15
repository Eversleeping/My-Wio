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

func TestUserInputResolutionIsThreadScoped(t *testing.T) {
	db, server := gatewayTestServer(t, "user-input")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/input", Name: "input"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.CreateThread(ctx, workspaces[0].ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateThread(ctx, workspaces[0].ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	send := func(threadID, kind, payload string) {
		raw, _ := json.Marshal(protocol.StreamEvent{StreamID: threadID, Kind: kind, Payload: json.RawMessage(payload)})
		if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: raw}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		send(id, "approval.requested", `{"request_id":"7","kind":"item/tool/requestUserInput","detail":{"questions":[{"id":"q","question":"Which?"}]}}`)
	}
	send(a.ID, "codex.serverRequest.resolved", `{"requestId":7}`)
	var pending int
	if err := db.DB.GetContext(ctx, &pending, "SELECT COUNT(*) FROM approvals WHERE status='pending'"); err != nil || pending != 1 {
		t.Fatalf("pending %d %v", pending, err)
	}
	var threadID string
	if err := db.DB.GetContext(ctx, &threadID, "SELECT thread_id FROM approvals WHERE status='pending'"); err != nil || threadID != b.ID {
		t.Fatalf("wrong session resolved: %s %v", threadID, err)
	}
	send(b.ID, "codex.turn.completed", `{"turn":{"status":"interrupted"}}`)
	if err := db.DB.GetContext(ctx, &pending, "SELECT COUNT(*) FROM approvals WHERE status='pending'"); err != nil || pending != 0 {
		t.Fatalf("interrupted question retained: %d %v", pending, err)
	}
}
