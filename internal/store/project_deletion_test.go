package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestProjectDeletionPlanReportsDependenciesAndPhysicalBlockers(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "delete-plan", "delete-plan")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "delete-plan.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "delete-plan", "https://example.com/delete-plan.git")
	if err != nil {
		t.Fatal(err)
	}
	managedID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/delete-plan", "managed", "primary", 1)
	insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/observed", "observed", "primary", 1)

	plan, err := database.ProjectDeletionPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RemotePreserved || plan.WorkspaceCount != 2 || plan.ManagedWorkspaceCount != 1 || plan.ObservedWorkspaceCount != 1 || plan.DirtyManagedWorkspaces != 1 {
		t.Fatalf("unexpected deletion plan: %#v", plan)
	}
	if !plan.CanDeleteMetadata || plan.CanDeleteManagedFiles || !hasDeletionBlocker(plan, "dirty-managed-workspaces") {
		t.Fatalf("dirty workspace blocker missing: %#v", plan)
	}

	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET dirty=0 WHERE id=?"), managedID); err != nil {
		t.Fatal(err)
	}
	threadID := NewID()
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO codex_threads(id,workspace_id,title,status) VALUES(?,?,?,'running')"), threadID, managedID, "active"); err != nil {
		t.Fatal(err)
	}
	targetID := NewID()
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO deployment_targets(id,project_id,server_id,environment,repository) VALUES(?,?,?,?,?)"), targetID, project.ID, server.ID, "production", "repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO deployments(id,target_id,commit_ref,status) VALUES(?,?,?,'running')"), NewID(), targetID, "main"); err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueResourceOperation(ctx, server.ID, "workspace.files", struct{}{}, "delete-plan-active", OperationResource{ProjectID: project.ID, WorkspaceID: managedID}, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = database.ProjectDeletionPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanDeleteMetadata || plan.CanDeleteManagedFiles || plan.ActiveAgentOperations != 1 || plan.ActiveCodexTasks != 1 || plan.ActiveDeployments != 1 {
		t.Fatalf("active dependencies did not block deletion: %#v", plan)
	}
	for _, code := range []string{"active-agent-operations", "active-codex-tasks", "active-deployments"} {
		if !hasDeletionBlocker(plan, code) {
			t.Fatalf("missing blocker %s: %#v", code, plan.Blockers)
		}
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET status='idle' WHERE id=?"), threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE deployments SET status='succeeded' WHERE target_id=?"), targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET status='offline',last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-2*ServerOnlineGracePeriod), server.ID); err != nil {
		t.Fatal(err)
	}
	plan, err = database.ProjectDeletionPlan(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanDeleteMetadata || plan.CanDeleteManagedFiles || plan.OfflineManagedServers != 1 || !hasDeletionBlocker(plan, "offline-managed-servers") {
		t.Fatalf("offline managed server blocker missing: %#v", plan)
	}
}

func TestDeleteProjectMetadataCascadesControlPlaneRecords(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "metadata-delete", "metadata-delete")
	project, err := database.CreateProject(ctx, "metadata-delete", "https://example.com/metadata-delete.git")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/metadata-delete", "observed", "primary", 1)
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO codex_threads(id,workspace_id,title,status) VALUES(?,?,?,'idle')"), NewID(), workspaceID, "idle"); err != nil {
		t.Fatal(err)
	}
	targetID := NewID()
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO deployment_targets(id,project_id,server_id,environment,repository) VALUES(?,?,?,?,?)"), targetID, project.ID, server.ID, "staging", "repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO deployments(id,target_id,commit_ref,status) VALUES(?,?,?,'succeeded')"), NewID(), targetID, "main"); err != nil {
		t.Fatal(err)
	}
	deleted, err := database.DeleteProjectMetadata(ctx, project.ID)
	if err != nil || !deleted {
		t.Fatalf("delete metadata returned %v, %v", deleted, err)
	}
	if _, err := database.Project(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project metadata still exists: %v", err)
	}
	for table, query := range map[string]string{
		"workspaces":         "SELECT COUNT(*) FROM workspaces WHERE project_id=?",
		"codex_threads":      "SELECT COUNT(*) FROM codex_threads WHERE workspace_id=?",
		"deployment_targets": "SELECT COUNT(*) FROM deployment_targets WHERE project_id=?",
	} {
		var count int
		arg := project.ID
		if table == "codex_threads" {
			arg = workspaceID
		}
		if err := database.DB.GetContext(ctx, &count, database.Q(query), arg); err != nil || count != 0 {
			t.Fatalf("%s was not cascaded: %d %v", table, count, err)
		}
	}
}

func TestManagedProjectDeletionStagesWorktreesBeforePrimary(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "staged-delete", "staged-delete")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "staged-delete.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "staged-delete", "")
	if err != nil {
		t.Fatal(err)
	}
	primaryID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/staged-delete", "managed", "primary", 0)
	worktreeID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/staged-delete-feature", "managed", "worktree", 0)

	operationIDs, deleted, err := database.QueueProjectManagedDeletion(ctx, project.ID)
	if err != nil || deleted || len(operationIDs) != 2 {
		t.Fatalf("unexpected queued deletion: %#v %v %v", operationIDs, deleted, err)
	}
	operations, err := database.ListProjectOperations(ctx, project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	primaryOperation := deletionOperationForWorkspace(t, operations, primaryID)
	worktreeOperation := deletionOperationForWorkspace(t, operations, worktreeID)
	if primaryOperation.Status != "waiting" || worktreeOperation.Status != "queued" {
		t.Fatalf("unexpected staged operation states: primary=%s worktree=%s", primaryOperation.Status, worktreeOperation.Status)
	}
	completeDeletionOperation(t, database, worktreeOperation, true, "")
	primaryOperation, err = database.Operation(ctx, primaryOperation.ID)
	if err != nil || primaryOperation.Status != "queued" {
		t.Fatalf("primary deletion was not released: %#v %v", primaryOperation, err)
	}
	if _, err := database.Workspace(ctx, worktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted worktree metadata remains: %v", err)
	}
	completeDeletionOperation(t, database, primaryOperation, true, "")
	if _, err := database.Project(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project remains after final deletion: %v", err)
	}
}

func TestManagedProjectDeletionFailureIsTrackableAndRetryable(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "retry-delete", "retry-delete")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "retry-delete.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "retry-delete", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/retry-delete", "managed", "primary", 0)
	operationIDs, _, err := database.QueueProjectManagedDeletion(ctx, project.ID)
	if err != nil || len(operationIDs) != 1 {
		t.Fatalf("queue delete: %#v %v", operationIDs, err)
	}
	operation, err := database.Operation(ctx, operationIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	completeDeletionOperation(t, database, operation, false, "permission denied")
	failedProject, err := database.Project(ctx, project.ID)
	if err != nil || failedProject.Status != "deletion_failed" || failedProject.ProvisionError != "permission denied" {
		t.Fatalf("deletion failure was not retained: %#v %v", failedProject, err)
	}
	if _, err := database.Workspace(ctx, workspaceID); err != nil {
		t.Fatalf("failed workspace metadata was removed: %v", err)
	}
	retryIDs, _, err := database.QueueProjectManagedDeletion(ctx, project.ID)
	if err != nil || len(retryIDs) != 1 || retryIDs[0] == operation.ID {
		t.Fatalf("retry was not queued: %#v %v", retryIDs, err)
	}
	old, err := database.Operation(ctx, operation.ID)
	if err != nil || old.Status != "superseded" || old.Result != "permission denied" {
		t.Fatalf("failed attempt was not preserved: %#v %v", old, err)
	}
	retry, err := database.Operation(ctx, retryIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	completeDeletionOperation(t, database, retry, true, "")
	if _, err := database.Project(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project remains after successful retry: %v", err)
	}
}

func TestManagedProjectDeletionNormalizesUnexpectedAgentStatus(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "unexpected-delete-status", "unexpected-delete-status")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "unexpected-delete-status.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "unexpected-delete-status", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/unexpected-delete-status", "managed", "primary", 0)
	operationIDs, _, err := database.QueueProjectManagedDeletion(ctx, project.ID)
	if err != nil || len(operationIDs) != 1 {
		t.Fatalf("queue delete: %#v %v", operationIDs, err)
	}
	operation, err := database.Operation(ctx, operationIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteProjectDeletion(ctx, operation, protocol.OperationResult{OperationID: operation.ID, Status: "partial", Message: "agent returned an unsupported state"}); err != nil {
		t.Fatal(err)
	}
	operation, err = database.Operation(ctx, operation.ID)
	if err != nil || operation.Status != "failed" {
		t.Fatalf("unexpected operation state: %#v %v", operation, err)
	}
	current, err := database.Project(ctx, project.ID)
	if err != nil || current.Status != "deletion_failed" {
		t.Fatalf("unexpected project state: %#v %v", current, err)
	}
	if _, err := database.Workspace(ctx, workspaceID); err != nil {
		t.Fatalf("workspace metadata was removed after invalid result status: %v", err)
	}
}

func insertDeletionWorkspace(t *testing.T, database *Store, projectID, serverID, path, mode, kind string, dirty int) string {
	t.Helper()
	id := NewID()
	if _, err := database.DB.ExecContext(context.Background(), database.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,branch,commit_sha,dirty) VALUES(?,?,?,?,?,?,'ready',?,'main','abc',?)`), id, projectID, serverID, path, path, mode, kind, dirty); err != nil {
		t.Fatal(err)
	}
	return id
}

func hasDeletionBlocker(plan ProjectDeletionPlan, code string) bool {
	for _, blocker := range plan.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func deletionOperationForWorkspace(t *testing.T, operations []Operation, workspaceID string) Operation {
	t.Helper()
	for _, operation := range operations {
		if operation.Kind == ProjectDeleteOperationKind && operation.WorkspaceID == workspaceID {
			return operation
		}
	}
	t.Fatalf("deletion operation for workspace %s not found", workspaceID)
	return Operation{}
}

func completeDeletionOperation(t *testing.T, database *Store, operation Operation, succeeded bool, message string) {
	t.Helper()
	status := "failed"
	var data json.RawMessage
	if succeeded {
		status = "succeeded"
		var command protocol.GitProjectDeleteCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			t.Fatal(err)
		}
		data, _ = json.Marshal(protocol.GitProjectDeleteResult{ProjectID: command.ProjectID, WorkspaceID: command.WorkspaceID, Path: command.Path, Removed: true})
	}
	if err := database.CompleteProjectDeletion(context.Background(), operation, protocol.OperationResult{OperationID: operation.ID, Status: status, Message: message, Data: data}); err != nil {
		t.Fatal(err)
	}
}
