package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
)

func TestFailedSteerIsVisibleWithoutFailingRunningTurn(t *testing.T) {
	db, server := gatewayTestServer(t, "steer-failed")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/steer", Name: "steer"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %#v %v", workspaces, err)
	}
	thread, err := db.CreateThread(ctx, workspaces[0].ID, "steer")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetThreadStatus(ctx, thread.ID, "running"); err != nil {
		t.Fatal(err)
	}
	id, err := db.QueueOperation(ctx, server.ID, "codex.turn.steer", protocol.SteerTurnCommand{ThreadID: thread.ID, CodexThread: "native", ExpectedTurnID: "turn", Prompt: "correction"}, "failed-steer")
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload, _ := json.Marshal(protocol.OperationResult{OperationID: id, Status: "failed", Message: "no active turn"})
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	current, err := db.Thread(ctx, thread.ID)
	if err != nil || current.Status != "running" {
		t.Fatalf("steer failure changed task: %#v %v", current, err)
	}
	events, err := db.RecentConversationEvents(ctx, thread.ID, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "codex.steer.failed" || !strings.Contains(string(events[0].Payload), "no active turn") {
		t.Fatalf("failure not visible: %#v %v", events, err)
	}
}
