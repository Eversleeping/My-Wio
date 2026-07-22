package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestDeploymentLifecycleManagement(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "deployable", "https://example.com/deployable.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL, PublicURL: "http://203.0.113.10:5000"})
	if err != nil {
		t.Fatal(err)
	}
	target.Environment = "staging"
	target.ComposeFile = "deploy/compose.yaml"
	target.PublicURL = "https://app.example.com"
	updated, err := database.UpdateDeploymentTarget(ctx, target)
	if err != nil || updated.Environment != "staging" || updated.ComposeFile != "deploy/compose.yaml" || updated.PublicURL != "https://app.example.com" {
		t.Fatalf("unexpected target update: %#v %v", updated, err)
	}
	deployment, err := database.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if deployment.PublicURL != "https://app.example.com" {
		t.Fatalf("deployment did not expose the target public URL: %#v", deployment)
	}
	if err := database.DeleteDeployment(ctx, deployment.ID); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("active deployment deletion returned %v", err)
	}
	if err := database.DeleteDeploymentTarget(ctx, target.ID); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("active target deletion returned %v", err)
	}
	for _, update := range []protocol.DeploymentStatus{
		{DeploymentID: deployment.ID, Status: "preparing", Message: "repository cloned", Content: "Cloning into release"},
		{DeploymentID: deployment.ID, Status: "succeeded", Message: "deployment is healthy", ResolvedCommit: "abc123", Content: "Release promoted"},
	} {
		if err := database.SaveDeploymentStatus(ctx, update); err != nil {
			t.Fatal(err)
		}
	}
	events, err := database.DeploymentEvents(ctx, deployment.ID)
	if err != nil || len(events) != 2 || events[0].Message != "repository cloned" || events[1].Content != "Release promoted" {
		t.Fatalf("unexpected deployment events: %#v %v", events, err)
	}
	completed, err := database.Deployment(ctx, deployment.ID)
	if err != nil || completed.Status != "succeeded" || completed.ResolvedCommit != "abc123" || completed.StartedAt == nil || completed.FinishedAt == nil {
		t.Fatalf("unexpected completed deployment: %#v %v", completed, err)
	}
	if err := database.DeleteDeployment(ctx, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if remaining, err := database.DeploymentEvents(ctx, deployment.ID); err != nil || len(remaining) != 0 {
		t.Fatalf("deployment events were not cascaded: %#v %v", remaining, err)
	}
	if err := database.DeleteDeploymentTarget(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentContainerOperationsTrackStateAndSerializeWrites(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "container-actions", "https://example.com/container-actions.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := database.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "pending" || target.ContainerAction != "deploy" || target.ContainerOperationID != "" {
		t.Fatalf("queued deployment did not mark containers pending: %#v %v", target, err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "succeeded", Message: "deployment is healthy", Content: "compose up"}); err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "running" || target.ContainerAction != "deploy" {
		t.Fatalf("successful deployment did not mark containers running: %#v %v", target, err)
	}

	operationID, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "stop", "v1:encrypted", "container-stop-once")
	if err != nil {
		t.Fatal(err)
	}
	duplicateID, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "stop", "v1:encrypted", "container-stop-once")
	if err != nil || duplicateID != operationID {
		t.Fatalf("container operation was not idempotent: %q %q %v", operationID, duplicateID, err)
	}
	if _, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "restart", "v1:encrypted", "container-restart-blocked"); !errors.Is(err, ErrDeploymentContainerActive) {
		t.Fatalf("parallel container operation returned %v", err)
	}
	if _, err := database.CreateDeployment(ctx, target.ID, "main"); !errors.Is(err, ErrDeploymentContainerActive) {
		t.Fatalf("deployment was not blocked by container operation: %v", err)
	}
	target.Environment = "staging"
	if _, err := database.UpdateDeploymentTarget(ctx, target); !errors.Is(err, ErrDeploymentContainerActive) {
		t.Fatalf("target update was not blocked by container operation: %v", err)
	}
	if err := database.DeleteDeploymentTarget(ctx, target.ID); !errors.Is(err, ErrDeploymentContainerActive) {
		t.Fatalf("target deletion was not blocked by container operation: %v", err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "pending" || target.ContainerAction != "stop" || target.ContainerOperationID != operationID {
		t.Fatalf("unexpected pending container state: %#v %v", target, err)
	}

	data, _ := json.Marshal(protocol.ContainerActionResult{TargetID: target.ID, Action: "stop", State: "stopped", Message: "Docker Compose project stopped", Content: "stopped service"})
	result := protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data}
	if err := database.CompleteDeploymentContainerOperation(ctx, operationID, result); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(ctx, result); err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "stopped" || target.ContainerMessage != "Docker Compose project stopped" {
		t.Fatalf("unexpected completed container state: %#v %v", target, err)
	}

	active, err := database.CreateDeployment(ctx, target.ID, "release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "remove", "v1:encrypted", "container-remove-blocked"); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("container operation was not blocked by deployment: %v", err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: active.ID, Status: "failed", Message: "failed"}); err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "unknown" || target.ContainerAction != "deploy" {
		t.Fatalf("failed deployment did not make container state unknown: %#v %v", target, err)
	}
	rollback, err := database.CreateDeployment(ctx, target.ID, "rollback")
	if err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "pending" || target.ContainerAction != "rollback" {
		t.Fatalf("queued rollback did not mark containers pending: %#v %v", target, err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: rollback.ID, Status: "rolled_back", Message: "rollback completed"}); err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "running" || target.ContainerAction != "rollback" {
		t.Fatalf("successful rollback did not mark containers running: %#v %v", target, err)
	}
}

func deploymentTestServer(t *testing.T, database *Store) Server {
	t.Helper()
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "deploy-node", []string{"/srv"}, "deploy-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "deploy-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "deploy-node.local", "deploy-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	return server
}
