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
	"github.com/wio-platform/wio/internal/store"
)

func TestAgentEventsAreServerScoped(t *testing.T) {
	db, server := gatewayTestServer(t, "isolation")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/repo", Name: "repo", RemoteURL: "https://example.com/repo.git"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v", err)
	}
	thread, err := db.CreateThread(ctx, workspaces[0].ID, "original")
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	_, events := hub.Subscribe()
	gateway := New(db, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, kind := range []string{"codex.turn.started", "codex.turn.completed", "codex.turn.failed", "codex.thread.name.updated", "approval.requested", "thread.bound", "codex.item.completed"} {
		payload, _ := json.Marshal(protocol.StreamEvent{StreamID: thread.ID, Kind: kind, Payload: json.RawMessage(`{"threadName":"foreign","request_id":"foreign-approval"}`)})
		if err := gateway.handle(ctx, "another-server", &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err == nil {
			t.Fatalf("foreign %s accepted", kind)
		}
	}
	updated, err := db.Thread(ctx, thread.ID)
	if err != nil || updated.Title != thread.Title || updated.Status != thread.Status || updated.CodexThreadID != thread.CodexThreadID {
		t.Fatalf("foreign event changed thread: %+v %v", updated, err)
	}
	var count int
	if err := db.DB.Get(&count, "SELECT COUNT(*) FROM approvals"); err != nil || count != 0 {
		t.Fatalf("foreign approval persisted: %d %v", count, err)
	}
	select {
	case <-events:
		t.Fatal("foreign event broadcast")
	default:
	}
	payload, _ := json.Marshal(protocol.StreamEvent{StreamID: "unknown", Kind: "codex.turn.started", Payload: json.RawMessage(`{}`)})
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err == nil {
		t.Fatal("unknown thread accepted")
	}
	payload, _ = json.Marshal(protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.started", Payload: json.RawMessage(`{}`)})
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, _ = db.Thread(ctx, thread.ID)
	if updated.Status != "running" {
		t.Fatal("owning Agent event rejected")
	}
}

func TestDeploymentStatusIsServerScoped(t *testing.T) {
	db, server := gatewayTestServer(t, "deployment-isolation")
	ctx := context.Background()
	project, err := db.CreateProject(ctx, "project", "https://example.com/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Repository: project.RemoteURL, Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := db.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	_, events := hub.Subscribe()
	gateway := New(db, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload, _ := json.Marshal(protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "succeeded"})
	if err := gateway.handle(ctx, "foreign", &protocol.AgentEnvelope{Kind: "deployment_status", PayloadJSON: payload}); err == nil {
		t.Fatal("foreign deployment status accepted")
	}
	updated, _ := db.Deployment(ctx, deployment.ID)
	if updated.Status != deployment.Status {
		t.Fatal("foreign deployment changed")
	}
	select {
	case <-events:
		t.Fatal("foreign deployment broadcast")
	default:
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "deployment_status", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
}
