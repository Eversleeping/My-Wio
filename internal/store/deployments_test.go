package store

import (
	"context"
	"database/sql"
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
	snapshot, err := database.DeploymentSnapshot(ctx, deployment.ID)
	if err != nil || snapshot.ResolvedCommit != "abc123" || snapshot.ComposeFile != "deploy/compose.yaml" || snapshot.Environment != "staging" {
		t.Fatalf("unexpected deployment snapshot: %#v %v", snapshot, err)
	}
	target.Environment = "production"
	target.ComposeFile = "compose.yaml"
	target.PublicURL = "https://changed.example.com"
	if _, err := database.UpdateDeploymentTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	unchanged, err := database.DeploymentSnapshot(ctx, deployment.ID)
	if err != nil || unchanged.Environment != "staging" || unchanged.ComposeFile != "deploy/compose.yaml" || unchanged.ConfiguredPublicURL != "https://app.example.com" {
		t.Fatalf("deployment snapshot changed with target configuration: %#v %v", unchanged, err)
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

func TestDetectedDeploymentPublicURLFallsBackAndConfiguredURLWins(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "detected-public-url", "https://example.com/detected-public-url.git")
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
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "succeeded", Message: "deployment is healthy", DetectedPublicURL: "http://203.0.113.10:5010"}); err != nil {
		t.Fatal(err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.PublicURL != "http://203.0.113.10:5010" || target.ConfiguredPublicURL != "" || target.DetectedPublicURL != "http://203.0.113.10:5010" {
		t.Fatalf("detected URL was not exposed as the fallback: %#v %v", target, err)
	}
	deployment, err = database.Deployment(ctx, deployment.ID)
	if err != nil || deployment.PublicURL != "http://203.0.113.10:5010" {
		t.Fatalf("deployment did not expose the detected URL: %#v %v", deployment, err)
	}
	snapshot, err := database.DeploymentSnapshot(ctx, deployment.ID)
	if err != nil || snapshot.DetectedPublicURL != "http://203.0.113.10:5010" {
		t.Fatalf("deployment snapshot did not expose the detected URL: %#v %v", snapshot, err)
	}
	target.PublicURL = "https://app.example.com"
	target, err = database.UpdateDeploymentTarget(ctx, target)
	if err != nil || target.PublicURL != "https://app.example.com" || target.ConfiguredPublicURL != "https://app.example.com" || target.DetectedPublicURL != "http://203.0.113.10:5010" {
		t.Fatalf("configured URL did not override the detected URL: %#v %v", target, err)
	}
}

func TestDeploymentConfigurationReviewAndRollbackBaseline(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "reviewable", "https://example.com/reviewable.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL, ComposeFile: "compose.yaml", PublicURL: "https://review.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := database.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "succeeded", ResolvedCommit: "review-commit"}); err != nil {
		t.Fatal(err)
	}
	target.Environment = "staging"
	target.ComposeFile = "deploy/compose.yaml"
	target.GitRef = "release"
	if _, err := database.UpdateDeploymentTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	review, err := database.DeploymentTargetReview(ctx, target.ID)
	if err != nil || !review.SnapshotAvailable || review.LastSuccessful == nil || len(review.Changes) < 3 {
		t.Fatalf("unexpected deployment configuration review: %#v %v", review, err)
	}
	rollback, err := database.CreateRollbackDeployment(ctx, target.ID, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollbackSnapshot, err := database.DeploymentSnapshot(ctx, rollback.ID)
	if err != nil || rollbackSnapshot.RollbackOfDeploymentID != deployment.ID || rollbackSnapshot.ResolvedCommit != "review-commit" || rollbackSnapshot.ComposeFile != "compose.yaml" {
		t.Fatalf("unexpected rollback snapshot: %#v %v", rollbackSnapshot, err)
	}
	if _, err := database.CreateRollbackDeployment(ctx, target.ID, "missing-deployment"); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("active rollback did not take precedence over missing source: %v", err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: rollback.ID, Status: "failed", Message: "rollback unavailable"}); err != nil {
		t.Fatal(err)
	}
	failed, err := database.CreateDeployment(ctx, target.ID, "release")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: failed.ID, Status: "failed", Message: "failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRollbackDeployment(ctx, target.ID, failed.ID); !errors.Is(err, ErrDeploymentSnapshotUnavailable) {
		t.Fatalf("failed deployment was accepted as rollback source: %v", err)
	}
}

func TestDeploymentSnapshotLocksSecretVersionWhenQueued(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	secretID, err := database.UpsertSecretSet(ctx, "", "production", "v1:first-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "versioned-secret", "https://example.com/versioned-secret.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, SecretSetID: secretID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := database.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !deployment.SnapshotAvailable {
		t.Fatal("queued deployment did not report its locked configuration snapshot")
	}
	queuedSnapshot, err := database.DeploymentSnapshot(ctx, deployment.ID)
	if err != nil || queuedSnapshot.SecretSetKeyVersion != 1 || queuedSnapshot.SecretSetUpdatedAt == nil {
		t.Fatalf("queued deployment did not lock secret version: %#v %v", queuedSnapshot, err)
	}
	if _, err := database.UpsertSecretSet(ctx, secretID, "production", "v1:second-secret"); err != nil {
		t.Fatal(err)
	}
	sets, err := database.ListSecretSets(ctx)
	if err != nil || len(sets) != 1 || sets[0].KeyVersion != 2 {
		t.Fatalf("secret set version was not incremented: %#v %v", sets, err)
	}
	firstCiphertext, firstUpdatedAt, err := database.SecretCiphertextVersion(ctx, secretID, 1)
	if err != nil || firstCiphertext != "v1:first-secret" || !firstUpdatedAt.Equal(*queuedSnapshot.SecretSetUpdatedAt) {
		t.Fatalf("historical secret version was not retained: %q %v %v", firstCiphertext, firstUpdatedAt, err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "succeeded", ResolvedCommit: "locked-secret"}); err != nil {
		t.Fatal(err)
	}
	finalSnapshot, err := database.DeploymentSnapshot(ctx, deployment.ID)
	if err != nil || finalSnapshot.SecretSetKeyVersion != 1 || finalSnapshot.ResolvedCommit != "locked-secret" {
		t.Fatalf("deployment success rewrote queued configuration: %#v %v", finalSnapshot, err)
	}
	review, err := database.DeploymentTargetReview(ctx, target.ID)
	if err != nil || len(review.Changes) == 0 {
		t.Fatalf("secret rotation was not visible in configuration review: %#v %v", review, err)
	}
	foundVersion := false
	for _, change := range review.Changes {
		if change.Field == "secret_set_key_version" && change.Previous == "1" && change.Current == "2" {
			foundVersion = true
		}
	}
	if !foundVersion {
		t.Fatalf("secret version change missing from review: %#v", review.Changes)
	}
}

func TestDeploymentHistoryDoesNotBorrowCurrentTargetWithoutSnapshot(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "legacy-history", "https://example.com/legacy-history.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL, PublicURL: "https://current.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO deployments(id,target_id,commit_ref,status) VALUES(?,?,?,'succeeded')"), "legacy-deployment", target.ID, "main"); err != nil {
		t.Fatal(err)
	}
	history, err := database.ListDeployments(ctx)
	if err != nil || len(history) != 1 {
		t.Fatalf("unexpected deployment history: %#v %v", history, err)
	}
	if history[0].SnapshotAvailable || history[0].ProjectName != "" || history[0].Environment != "" || history[0].PublicURL != "" {
		t.Fatalf("legacy deployment borrowed mutable target configuration: %#v", history[0])
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

func TestDeploymentTargetDeletionWaitsForAgentCleanup(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	server := deploymentTestServer(t, database)
	project, err := database.CreateProject(ctx, "delete-target", "https://example.com/delete-target.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}

	failedOperation, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "delete", "v1:encrypted", "target-delete-failed")
	if err != nil {
		t.Fatal(err)
	}
	failedData, _ := json.Marshal(protocol.ContainerActionResult{TargetID: target.ID, Action: "delete", State: "failed", Message: "compose down failed"})
	if err := database.CompleteDeploymentContainerOperation(ctx, failedOperation, protocol.OperationResult{OperationID: failedOperation, Status: "failed", Message: "compose down failed", Data: failedData}); err != nil {
		t.Fatal(err)
	}
	remaining, err := database.DeploymentTarget(ctx, target.ID)
	if err != nil || remaining.ContainerStatus != "failed" {
		t.Fatalf("failed cleanup removed or hid the target: %#v %v", remaining, err)
	}

	operationID, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "delete", "v1:encrypted", "target-delete-succeeded")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(protocol.ContainerActionResult{TargetID: target.ID, Action: "delete", State: "removed", Message: "deployment files deleted"})
	if err := database.CompleteDeploymentContainerOperation(ctx, operationID, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DeploymentTarget(ctx, target.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("target metadata remained after cleanup: %v", err)
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
