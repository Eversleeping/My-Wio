package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestOperationInterruptResultReleasesSnapshotAndPublishesOnce(t *testing.T) {
	db, server := gatewayTestServer(t, "interrupt-gateway")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/interrupt", Name: "interrupt"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v", err)
	}
	w := workspaces[0]
	if err := db.BeginWorkspaceGitRefresh(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	id, err := db.QueueResourceOperation(ctx, server.ID, "git.workspace.inspect", protocol.GitWorkspaceInspectCommand{WorkspaceID: w.ID, Path: w.Path}, "interrupt-gateway-target", store.OperationResource{WorkspaceID: w.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	_, events := hub.Subscribe()
	gateway := New(db, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload, _ := json.Marshal(protocol.OperationResult{OperationID: id, Status: "cancelled", Message: "interrupted by user"})
	if err := gateway.handle(ctx, "foreign-server", &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err == nil {
		t.Fatal("foreign Agent cancelled operation")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := db.WorkspaceGitSnapshot(ctx, w.ID)
	if err != nil || snapshot.Status != "failed" || snapshot.Error != "interrupted by user" {
		t.Fatalf("snapshot still refreshing: %#v %v", snapshot, err)
	}
	select {
	case event := <-events:
		if event.Kind != "operation.cancelled" {
			t.Fatalf("wrong event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation not published")
	}
	select {
	case <-events:
		t.Fatal("duplicate cancellation published")
	default:
	}
	late, _ := json.Marshal(protocol.OperationResult{OperationID: id, Status: "succeeded", Data: json.RawMessage(`{"branches":[]}`)})
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: late}); err != nil {
		t.Fatal(err)
	}
	operation, err := db.Operation(ctx, id)
	if err != nil || operation.Status != "cancelled" {
		t.Fatalf("late result resurrected operation: %#v %v", operation, err)
	}
}
