package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestBlankProjectOperationResultCommitsWorkspaceAndPublishes(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "blank-gateway", []string{"/srv"}, "blank-gateway-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "blank-gateway-enrollment")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "blank-gateway.local", "blank-gateway-agent")
	if err != nil {
		t.Fatal(err)
	}
	provision, err := database.CreateBlankProject(ctx, server.ID, "gateway-project", "gateway-project", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	resultData, err := json.Marshal(protocol.GitProjectCreateResult{Path: "/var/lib/wio-agent/projects/gateway-project", Branch: "main", Unborn: true})
	if err != nil {
		t.Fatal(err)
	}
	resultPayload, err := json.Marshal(protocol.OperationResult{OperationID: provision.OperationID, Status: "succeeded", Data: resultData})
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	subscriptionID, events := hub.Subscribe()
	t.Cleanup(func() { hub.Unsubscribe(subscriptionID) })
	gateway := New(database, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: resultPayload}); err != nil {
		t.Fatal(err)
	}
	project, err := database.Project(ctx, provision.Project.ID)
	if err != nil || project.Status != "ready" {
		t.Fatalf("project was not marked ready: %#v %v", project, err)
	}
	workspace, err := database.Workspace(ctx, provision.WorkspaceID)
	if err != nil || workspace.ManagementMode != "managed" || workspace.Path != "/var/lib/wio-agent/projects/gateway-project" {
		t.Fatalf("workspace was not committed: %#v %v", workspace, err)
	}
	select {
	case event := <-events:
		if event.Kind != "operation.succeeded" || event.StreamID != server.ID {
			t.Fatalf("unexpected blank project event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blank project event")
	}
}

func TestBlankProjectOperationFailureAndResultMismatchAreRetriable(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "blank-failure", []string{"/srv"}, "blank-failure-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "blank-failure-enrollment")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "blank-failure.local", "blank-failure-agent")
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	gateway := New(database, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	provision, err := database.CreateBlankProject(ctx, server.ID, "failed-project", "failed-project", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	failurePayload, _ := json.Marshal(protocol.OperationResult{OperationID: provision.OperationID, Status: "failed", Message: "destination already exists"})
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: failurePayload}); err != nil {
		t.Fatal(err)
	}
	failed, err := database.Project(ctx, provision.Project.ID)
	if err != nil || failed.Status != "failed" || failed.ProvisionError != "destination already exists" {
		t.Fatalf("unexpected failed project: %#v %v", failed, err)
	}
	retry, err := database.RetryBlankProject(ctx, provision.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	badResult, _ := json.Marshal(protocol.GitProjectCreateResult{Path: "/var/lib/wio-agent/projects/failed-project", Branch: "wrong", Unborn: true})
	badPayload, _ := json.Marshal(protocol.OperationResult{OperationID: retry.OperationID, Status: "succeeded", Data: badResult})
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: badPayload}); err != nil {
		t.Fatal(err)
	}
	partial, err := database.Project(ctx, provision.Project.ID)
	if err != nil || partial.Status != "failed" || partial.ProvisionError == "" {
		t.Fatalf("result mismatch should retain retryable failure: %#v %v", partial, err)
	}
}
