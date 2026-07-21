package store

import (
	"context"
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
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}
	target.Environment = "staging"
	target.ComposeFile = "deploy/compose.yaml"
	updated, err := database.UpdateDeploymentTarget(ctx, target)
	if err != nil || updated.Environment != "staging" || updated.ComposeFile != "deploy/compose.yaml" {
		t.Fatalf("unexpected target update: %#v %v", updated, err)
	}
	deployment, err := database.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
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
