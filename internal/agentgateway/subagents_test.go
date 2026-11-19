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

func TestNativeChildEventsPreserveOwnershipRedactionAndDeduplication(t *testing.T) {
	ctx := context.Background()
	db, server := gatewayTestServer(t, "subagent-events")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/children", Name: "children"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	parent, err := db.CreateThread(ctx, workspaces[0].ID, "Parent")
	if err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	event := protocol.StreamEvent{EventID: "child-event", StreamID: parent.ID, Kind: protocol.EventSessionSubagentUpdated, Payload: json.RawMessage(`{"id":"child","status":"running","entry":{"id":"tool","kind":"tool","input":{"api_key":"private-value"}}}`)}
	if err := g.handleSessionEvent(ctx, "wrong-server", "codex", event); err == nil {
		t.Fatal("foreign event accepted")
	}
	for range 2 {
		if err := g.handleSessionEvent(ctx, server.ID, "codex", event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := db.SubagentEvents(ctx, parent.ID, "child", 0, 100)
	if err != nil || len(events) != 1 || strings.Contains(string(events[0].Payload), "private-value") {
		t.Fatalf("event boundary failed: %+v %v", events, err)
	}
	current, err := db.Thread(ctx, parent.ID)
	if err != nil || current.Status != parent.Status {
		t.Fatal("child changed parent lifecycle")
	}
}
