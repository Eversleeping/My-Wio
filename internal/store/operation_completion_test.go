package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func assertOperationStatus(t *testing.T, database *Store, id, status, result string) {
	t.Helper()
	operation, err := database.Operation(context.Background(), id)
	if err != nil || operation.Status != status || operation.Result != result {
		t.Fatalf("operation = %q %q, want %q %q (%v)", operation.Status, operation.Result, status, result, err)
	}
}

func TestCredentialCompletionIgnoresFinishedOperations(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "credentials", "credentials-token")
	profile, err := database.SaveCredentialProfile(ctx, CredentialProfile{Kind: "codex", Name: "codex"}, "v1:profile")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := database.QueueCredentialUpdate(ctx, server.ID, "v1:payload", profile.ID, nil, "credentials-cancelled", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := database.CancelOperation(ctx, cancelled, "cancelled by user"); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if err := database.CompleteCredentialUpdate(ctx, protocol.OperationResult{OperationID: cancelled, Status: "succeeded"}); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("late result for a cancelled update: %v", err)
	}
	assertOperationStatus(t, database, cancelled, "cancelled", "cancelled by user")
	if bound, err := database.Server(ctx, server.ID); err != nil || bound.CodexProfileID != "" {
		t.Fatalf("cancelled update bound credentials: %q %v", bound.CodexProfileID, err)
	}

	live, err := database.QueueCredentialUpdate(ctx, server.ID, "v1:payload", profile.ID, nil, "credentials-live", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCredentialUpdate(ctx, protocol.OperationResult{OperationID: live, Status: "succeeded", Message: "configured"}); err != nil {
		t.Fatal(err)
	}
	if bound, err := database.Server(ctx, server.ID); err != nil || bound.CodexProfileID != profile.ID {
		t.Fatalf("active update did not bind credentials: %q %v", bound.CodexProfileID, err)
	}
	// A duplicate delivery cannot flip the recorded outcome.
	if err := database.CompleteCredentialUpdate(ctx, protocol.OperationResult{OperationID: live, Status: "failed", Message: "duplicate"}); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("duplicate result: %v", err)
	}
	assertOperationStatus(t, database, live, "succeeded", "configured")
}

func managedLifecycleWorkspace(t *testing.T, database *Store, name string) (Server, Workspace) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, database, name, name+"-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/" + name, Name: name, Branch: "main", CommitSHA: "abc123"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range workspaces {
		if workspace.ServerID == server.ID {
			if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
				t.Fatal(err)
			}
			workspace.ManagementMode = "managed"
			return server, workspace
		}
	}
	t.Fatal("workspace missing")
	return Server{}, Workspace{}
}

func TestWorkspaceLifecycleCompletionIgnoresFinishedOperations(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	_, workspace := managedLifecycleWorkspace(t, database, "lifecycle-race")

	// The reconcile path fails a lost running move and marks the workspace
	// partial; the Agent's late success must not then move the record.
	move := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, Action: "move", SourcePath: workspace.Path, TargetPath: "/srv/managed/moved"}
	moveID, err := database.QueueWorkspaceLifecycle(ctx, workspace, move)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, moveID); err != nil {
		t.Fatal(err)
	}
	moveOperation, _ := database.Operation(ctx, moveID)
	lost := protocol.OperationResult{OperationID: moveID, Status: "failed", Message: protocol.OperationLostMessage}
	if err := database.FailWorkspaceLifecycle(ctx, moveOperation, move, lost, "partial"); err != nil {
		t.Fatal(err)
	}
	if current, _ := database.Workspace(ctx, workspace.ID); current.Status != "partial" {
		t.Fatalf("reconcile did not mark the workspace partial: %#v", current)
	}
	moved := protocol.GitWorkspaceLifecycleResult{WorkspaceID: workspace.ID, Action: "move", SourcePath: workspace.Path, TargetPath: move.TargetPath}
	data, _ := json.Marshal(moved)
	late := protocol.OperationResult{OperationID: moveID, Status: "succeeded", Data: data}
	if err := database.CompleteWorkspaceLifecycle(ctx, moveOperation, move, moved, late); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("late success: %v", err)
	}
	// The gateway falls back to marking the workspace partial.
	if err := database.FailWorkspaceLifecycle(ctx, moveOperation, move, protocol.OperationResult{OperationID: moveID, Status: "failed", Message: "commit failed"}, "partial"); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("fallback after late success: %v", err)
	}
	current, err := database.Workspace(ctx, workspace.ID)
	if err != nil || current.Path != workspace.Path || current.Status != "partial" || current.GitError != protocol.OperationLostMessage {
		t.Fatalf("stale result changed the workspace: %#v %v", current, err)
	}
	assertOperationStatus(t, database, moveID, "failed", protocol.OperationLostMessage)

	// A duplicate success delivery must not turn a committed copy into a
	// failed operation with a partial workspace.
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET status='ready',git_error='' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	copyCommand := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, Action: "copy", SourcePath: workspace.Path, TargetPath: "/srv/managed/copy", TargetWorkspaceID: NewID()}
	copyID, err := database.QueueWorkspaceLifecycle(ctx, workspace, copyCommand)
	if err != nil {
		t.Fatal(err)
	}
	copyOperation, _ := database.Operation(ctx, copyID)
	copied := protocol.GitWorkspaceLifecycleResult{WorkspaceID: workspace.ID, TargetWorkspaceID: copyCommand.TargetWorkspaceID, Action: "copy", SourcePath: workspace.Path, TargetPath: copyCommand.TargetPath}
	data, _ = json.Marshal(copied)
	success := protocol.OperationResult{OperationID: copyID, Status: "succeeded", Message: "copied", Data: data}
	if err := database.CompleteWorkspaceLifecycle(ctx, copyOperation, copyCommand, copied, success); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteWorkspaceLifecycle(ctx, copyOperation, copyCommand, copied, success); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("duplicate success: %v", err)
	}
	if err := database.FailWorkspaceLifecycle(ctx, copyOperation, copyCommand, protocol.OperationResult{OperationID: copyID, Status: "failed", Message: "commit failed"}, "partial"); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("fallback after duplicate success: %v", err)
	}
	assertOperationStatus(t, database, copyID, "succeeded", "copied")
	if source, _ := database.Workspace(ctx, workspace.ID); source.Status != "ready" {
		t.Fatalf("duplicate success marked the source workspace: %#v", source)
	}
	if target, err := database.Workspace(ctx, copyCommand.TargetWorkspaceID); err != nil || target.Path != copyCommand.TargetPath {
		t.Fatalf("copy was not kept: %#v %v", target, err)
	}
}

func TestWorkspaceCloneCompletionIgnoresFinishedOperations(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	_, source := managedLifecycleWorkspace(t, database, "clone-source")
	target := createOperationTestServer(t, database, "clone-target", "clone-target-token")
	queue := func(name string) (Operation, protocol.GitWorkspaceCloneCommand, protocol.GitWorkspaceCloneResult) {
		t.Helper()
		command := protocol.GitWorkspaceCloneCommand{WorkspaceID: NewID(), ProjectID: source.ProjectID, Name: name, Destination: "/srv/managed/" + name, RemoteURL: "https://example.com/team/source.git", Branch: "main", ExpectedHead: "abc123"}
		id, err := database.QueueCrossServerWorkspaceClone(ctx, source, target.ID, command)
		if err != nil {
			t.Fatal(err)
		}
		operation, err := database.Operation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return operation, command, protocol.GitWorkspaceCloneResult{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: command.Destination, Branch: command.Branch, CommitSHA: command.ExpectedHead}
	}

	cancelled, command, result := queue("cancelled-copy")
	if ok, err := database.CancelOperation(ctx, cancelled.ID, "cancelled by user"); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if err := database.CompleteCrossServerWorkspaceClone(ctx, cancelled, command, result, protocol.OperationResult{OperationID: cancelled.ID, Status: "succeeded"}); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("late success: %v", err)
	}
	if err := database.FailCrossServerWorkspaceClone(ctx, cancelled, protocol.OperationResult{OperationID: cancelled.ID, Status: "failed", Message: "commit failed"}, "partial"); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("fallback after late success: %v", err)
	}
	if _, err := database.Workspace(ctx, command.WorkspaceID); err == nil {
		t.Fatal("cancelled clone created a workspace")
	}
	if current, _ := database.Workspace(ctx, source.ID); current.Status != "copying" {
		t.Fatalf("stale result changed the source workspace: %#v", current)
	}
	assertOperationStatus(t, database, cancelled.ID, "cancelled", "cancelled by user")

	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET status='ready' WHERE id=?"), source.ID); err != nil {
		t.Fatal(err)
	}
	live, command, result := queue("live-copy")
	success := protocol.OperationResult{OperationID: live.ID, Status: "succeeded", Message: "cloned"}
	if err := database.CompleteCrossServerWorkspaceClone(ctx, live, command, result, success); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCrossServerWorkspaceClone(ctx, live, command, result, success); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("duplicate success: %v", err)
	}
	if err := database.FailCrossServerWorkspaceClone(ctx, live, protocol.OperationResult{OperationID: live.ID, Status: "failed", Message: "commit failed"}, "partial"); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("fallback after duplicate success: %v", err)
	}
	assertOperationStatus(t, database, live.ID, "succeeded", "cloned")
	if current, _ := database.Workspace(ctx, source.ID); current.Status != "ready" {
		t.Fatalf("duplicate success marked the source workspace: %#v", current)
	}
}
