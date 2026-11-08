package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

// A result can arrive after its operation already finished: cancelled, failed
// by a reconcile, or delivered twice. It must not apply project side effects.
func TestStaleBlankProjectResultsDoNotChangeProject(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	server := createOperationTestServer(t, database, "stale-blank-server", "stale-blank-token")
	provision, err := database.CreateBlankProject(ctx, server.ID, "stale-blank", "apps/stale-blank", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, provision.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE agent_operations SET status='cancelled',result='cancelled by user' WHERE id=?"), operation.ID); err != nil {
		t.Fatal(err)
	}
	result := protocol.GitProjectCreateResult{Path: "/var/lib/wio-agent/projects/apps/stale-blank", Branch: "main", Unborn: true}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	err = database.CommitBlankProject(ctx, operation, command, result, protocol.OperationResult{OperationID: operation.ID, Status: "succeeded", Data: payload})
	if !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("stale success returned %v, want ErrOperationNotActive", err)
	}
	if _, err := database.Workspace(ctx, provision.WorkspaceID); err == nil {
		t.Fatal("stale success created the workspace")
	}
	err = database.FailBlankProject(ctx, operation, command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: "late failure"}, "partial")
	if !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("stale failure returned %v, want ErrOperationNotActive", err)
	}
	project, err := database.Project(ctx, provision.Project.ID)
	if err != nil || project.Status != "provisioning" || project.ProvisionError != "" {
		t.Fatalf("stale results changed the project: %#v %v", project, err)
	}
	stored, err := database.Operation(ctx, operation.ID)
	if err != nil || stored.Status != "cancelled" || stored.Result != "cancelled by user" {
		t.Fatalf("stale results overwrote the cancelled operation: %#v %v", stored, err)
	}
}

func TestStaleProjectDeletionResultDoesNotDeleteWorkspace(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "stale-delete", "stale-delete")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "stale-delete.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "stale-delete", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertDeletionWorkspace(t, database, project.ID, server.ID, "/srv/stale-delete", "managed", "primary", 0)
	operationIDs, _, err := database.QueueProjectManagedDeletion(ctx, project.ID)
	if err != nil || len(operationIDs) != 1 {
		t.Fatalf("queue delete: %#v %v", operationIDs, err)
	}
	operation, err := database.Operation(ctx, operationIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE agent_operations SET status='failed',result='Agent lost track of this operation' WHERE id=?"), operation.ID); err != nil {
		t.Fatal(err)
	}
	var command protocol.GitProjectDeleteCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(protocol.GitProjectDeleteResult{ProjectID: command.ProjectID, WorkspaceID: command.WorkspaceID, Path: command.Path, Removed: true})
	if err != nil {
		t.Fatal(err)
	}
	err = database.CompleteProjectDeletion(ctx, operation, protocol.OperationResult{OperationID: operation.ID, Status: "succeeded", Data: data})
	if !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("stale deletion result returned %v, want ErrOperationNotActive", err)
	}
	if _, err := database.Workspace(ctx, workspaceID); err != nil {
		t.Fatalf("stale deletion result removed the workspace: %v", err)
	}
	if _, err := database.Project(ctx, project.ID); err != nil {
		t.Fatalf("stale deletion result removed the project: %v", err)
	}
	stored, err := database.Operation(ctx, operation.ID)
	if err != nil || stored.Status != "failed" || stored.Result != "Agent lost track of this operation" {
		t.Fatalf("stale deletion result overwrote the operation: %#v %v", stored, err)
	}
}
