package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWorkspaceLifecycleStoreMoveCopyAndSerialization(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "lifecycle", "lifecycle-token")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "lifecycle", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/source", Name: "source", Branch: "main", CommitSHA: "abc123"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	workspace.ManagementMode = "managed"
	updated, err := database.UpdateWorkspaceDisplayName(ctx, workspace.ID, "Primary checkout")
	if err != nil || updated.DisplayName != "Primary checkout" {
		t.Fatalf("display name was not updated: %#v %v", updated, err)
	}

	move := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, Action: "move", SourcePath: workspace.Path, TargetPath: "/srv/managed/moved", WorkspaceKind: workspace.Kind}
	operationID, err := database.QueueWorkspaceLifecycle(ctx, workspace, move)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueWorkspaceLifecycle(ctx, workspace, move); !errors.Is(err, ErrWorkspaceWriteActive) {
		t.Fatalf("concurrent lifecycle operation was not rejected: %v", err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.WorkspaceWrite != 1 || operation.ProjectID != workspace.ProjectID {
		t.Fatalf("unexpected lifecycle operation: %#v %v", operation, err)
	}
	queued, err := database.Workspace(ctx, workspace.ID)
	if err != nil || queued.Status != "moving" {
		t.Fatalf("move status was not persisted: %#v %v", queued, err)
	}
	moveResult := protocol.GitWorkspaceLifecycleResult{WorkspaceID: workspace.ID, Action: "move", SourcePath: workspace.Path, TargetPath: move.TargetPath}
	moveData, _ := json.Marshal(moveResult)
	if err := database.CompleteWorkspaceLifecycle(ctx, operation, move, moveResult, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: moveData}); err != nil {
		t.Fatal(err)
	}
	workspace, err = database.Workspace(ctx, workspace.ID)
	if err != nil || workspace.Path != move.TargetPath || workspace.Status != "ready" {
		t.Fatalf("move was not committed: %#v %v", workspace, err)
	}

	copyCommand := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, TargetWorkspaceID: NewID(), ProjectID: workspace.ProjectID, Action: "copy", SourcePath: workspace.Path, TargetPath: "/srv/managed/copied", WorkspaceKind: workspace.Kind}
	copyID, err := database.QueueWorkspaceLifecycle(ctx, workspace, copyCommand)
	if err != nil {
		t.Fatal(err)
	}
	copyOperation, _ := database.Operation(ctx, copyID)
	queued, err = database.Workspace(ctx, workspace.ID)
	if err != nil || queued.Status != "copying" {
		t.Fatalf("copy status was not persisted: %#v %v", queued, err)
	}
	copyResult := protocol.GitWorkspaceLifecycleResult{WorkspaceID: workspace.ID, TargetWorkspaceID: copyCommand.TargetWorkspaceID, Action: "copy", SourcePath: workspace.Path, TargetPath: copyCommand.TargetPath}
	copyData, _ := json.Marshal(copyResult)
	if err := database.CompleteWorkspaceLifecycle(ctx, copyOperation, copyCommand, copyResult, protocol.OperationResult{OperationID: copyID, Status: "succeeded", Data: copyData}); err != nil {
		t.Fatal(err)
	}
	copied, err := database.Workspace(ctx, copyCommand.TargetWorkspaceID)
	if err != nil || copied.Path != copyCommand.TargetPath || copied.ManagementMode != "managed" || copied.ServerID != workspace.ServerID {
		t.Fatalf("copy was not committed: %#v %v", copied, err)
	}
	deleteCommand := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: copied.ID, ProjectID: copied.ProjectID, Action: "delete", SourcePath: copied.Path, WorkspaceKind: copied.Kind}
	deleteID, err := database.QueueWorkspaceLifecycle(ctx, copied, deleteCommand)
	if err != nil {
		t.Fatal(err)
	}
	deleteOperation, _ := database.Operation(ctx, deleteID)
	queued, err = database.Workspace(ctx, copied.ID)
	if err != nil || queued.Status != "deleting" {
		t.Fatalf("delete status was not persisted: %#v %v", queued, err)
	}
	deleteResult := protocol.GitWorkspaceLifecycleResult{WorkspaceID: copied.ID, Action: "delete", SourcePath: copied.Path}
	deleteData, _ := json.Marshal(deleteResult)
	if err := database.CompleteWorkspaceLifecycle(ctx, deleteOperation, deleteCommand, deleteResult, protocol.OperationResult{OperationID: deleteID, Status: "succeeded", Data: deleteData}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Workspace(ctx, copied.ID); !IsNotFound(err) {
		t.Fatalf("physically deleted workspace record remains: %v", err)
	}
	completedDelete, err := database.Operation(ctx, deleteID)
	if err != nil || completedDelete.Status != "succeeded" || completedDelete.WorkspaceID != "" {
		t.Fatalf("delete operation was not retained after workspace removal: %#v %v", completedDelete, err)
	}
}

func TestWorkspaceDeletionPlanAndMetadataRemoval(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "deletion", "deletion-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/observed", Name: "observed", Dirty: true}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	plan, err := database.WorkspaceDeletionPlan(ctx, workspace.ID, false)
	if err != nil || !plan.CanRemoveRecord || plan.CanDeleteFiles || plan.Managed || len(plan.RecordBlockers) != 0 || len(plan.FileBlockers) != 2 || plan.FileBlockers[0] != "workspace is not managed" || plan.FileBlockers[1] != "workspace has uncommitted changes" {
		t.Fatalf("unexpected observed deletion plan: %#v %v", plan, err)
	}
	if err := database.RemoveWorkspaceRecord(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Workspace(ctx, workspace.ID); !IsNotFound(err) {
		t.Fatalf("workspace record still exists: %v", err)
	}
}

func TestWorkspaceDeletionPlanBlocksDependenciesAndDirtyFiles(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "blocked-delete", "blocked-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/dirty", Name: "dirty", Dirty: true}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateThread(ctx, workspace.ID, "active session"); err != nil {
		t.Fatal(err)
	}
	plan, err := database.WorkspaceDeletionPlan(ctx, workspace.ID, true)
	if err != nil || plan.CanRemoveRecord || plan.CanDeleteFiles || plan.ThreadCount != 1 {
		t.Fatalf("dependent workspace was not blocked: %#v %v", plan, err)
	}
}

func TestCrossServerWorkspaceCloneQueuesOnTargetAndCommitsManagedWorkspace(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	sourceServer := createOperationTestServer(t, database, "clone-source", "clone-source-token")
	targetServer := createOperationTestServer(t, database, "clone-target", "clone-target-token")
	if err := database.UpsertInventory(ctx, sourceServer.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/source", Name: "source", Branch: "main", CommitSHA: "abc123"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	source := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), source.ID); err != nil {
		t.Fatal(err)
	}
	source.ManagementMode = "managed"
	command := protocol.GitWorkspaceCloneCommand{WorkspaceID: NewID(), ProjectID: source.ProjectID, Name: "source-copy", Destination: "/srv/managed/copy", RemoteURL: "https://example.com/team/source.git", Branch: "main", ExpectedHead: "abc123"}
	operationID, err := database.QueueCrossServerWorkspaceClone(ctx, source, targetServer.ID, command)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.ServerID != targetServer.ID || operation.WorkspaceID != source.ID || operation.WorkspaceWrite != 1 {
		t.Fatalf("unexpected cross-server operation: %#v %v", operation, err)
	}
	result := protocol.GitWorkspaceCloneResult{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: command.Destination, Branch: command.Branch, CommitSHA: command.ExpectedHead}
	data, _ := json.Marshal(result)
	if err := database.CompleteCrossServerWorkspaceClone(ctx, operation, command, result, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data}); err != nil {
		t.Fatal(err)
	}
	cloned, err := database.Workspace(ctx, command.WorkspaceID)
	if err != nil || cloned.ServerID != targetServer.ID || cloned.ProjectID != source.ProjectID || cloned.Path != command.Destination || cloned.ManagementMode != "managed" || cloned.CommitSHA != command.ExpectedHead {
		t.Fatalf("cross-server workspace was not committed: %#v %v", cloned, err)
	}
	source, err = database.Workspace(ctx, source.ID)
	if err != nil || source.Status != "ready" {
		t.Fatalf("source workspace was not released: %#v %v", source, err)
	}
}
