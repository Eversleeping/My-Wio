package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"strings"
	"time"
)

func (g *Gateway) completeGitWorkspaceClone(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.GitWorkspaceCloneCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status != "succeeded" {
		if result.Message == "" {
			result.Message = "agent failed to clone workspace"
		}
		if err := g.store.FailCrossServerWorkspaceClone(ctx, operation, *result, uncertainStatus(outcomeUnknown, "ready")); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	var cloned protocol.GitWorkspaceCloneResult
	if err := json.Unmarshal(result.Data, &cloned); err != nil {
		result.Status = "failed"
		result.Message = "invalid workspace clone result: " + err.Error()
		if err := g.store.FailCrossServerWorkspaceClone(ctx, operation, *result, "partial"); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	if err := g.store.CompleteCrossServerWorkspaceClone(ctx, operation, command, cloned, *result); err != nil {
		result.Status = "failed"
		result.Message = "workspace cloned but control-plane commit failed: " + err.Error()
		if partialErr := g.store.FailCrossServerWorkspaceClone(ctx, operation, *result, "partial"); partialErr != nil {
			return fmt.Errorf("commit workspace clone: %v; mark partial: %w", err, partialErr)
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	return publishOperationResult(ctx, g, serverID, *result)
}

func (g *Gateway) completeGitWorkspaceLifecycle(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.GitWorkspaceLifecycleCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status != "succeeded" {
		if result.Message == "" {
			result.Message = "agent failed to update workspace"
		}
		if err := g.store.FailWorkspaceLifecycle(ctx, operation, command, *result, uncertainStatus(outcomeUnknown, "ready")); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	var lifecycleResult protocol.GitWorkspaceLifecycleResult
	if err := json.Unmarshal(result.Data, &lifecycleResult); err != nil {
		result.Status = "failed"
		result.Message = "invalid workspace lifecycle result: " + err.Error()
		if err := g.store.FailWorkspaceLifecycle(ctx, operation, command, *result, "partial"); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	if err := g.store.CompleteWorkspaceLifecycle(ctx, operation, command, lifecycleResult, *result); err != nil {
		result.Status = "failed"
		result.Message = "workspace files changed but control-plane commit failed: " + err.Error()
		if partialErr := g.store.FailWorkspaceLifecycle(ctx, operation, command, *result, "partial"); partialErr != nil {
			return fmt.Errorf("commit workspace lifecycle: %v; mark partial: %w", err, partialErr)
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	return publishOperationResult(ctx, g, serverID, *result)
}

func (g *Gateway) completeGitProjectCreate(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status != "succeeded" {
		if result.Message == "" {
			result.Message = "agent failed to create blank project"
		}
		if err := g.store.FailBlankProject(ctx, operation, command, *result, uncertainStatus(outcomeUnknown, "failed")); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	var created protocol.GitProjectCreateResult
	if err := json.Unmarshal(result.Data, &created); err != nil {
		result.Status = "failed"
		result.Message = "invalid blank project result: " + err.Error()
		if err := g.store.FailBlankProject(ctx, operation, command, *result, "failed"); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	if err := store.ValidateBlankProjectResult(command, created); err != nil {
		result.Status = "failed"
		result.Message = err.Error()
		if err := g.store.FailBlankProject(ctx, operation, command, *result, "failed"); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	if err := g.store.CommitBlankProject(ctx, operation, command, created, *result); err != nil {
		// The agent has already created the directory. Preserve it and
		// leave a retryable partial project when the DB transaction fails.
		result.Status = "failed"
		result.Message = "project created on server but could not persist workspace: " + err.Error()
		if partialErr := g.store.FailBlankProject(ctx, operation, command, *result, "partial"); partialErr != nil {
			return fmt.Errorf("persist blank project: %v; mark partial: %w", err, partialErr)
		}
		return publishOperationResult(ctx, g, serverID, *result)
	}
	return publishOperationResult(ctx, g, serverID, *result)
}

func (g *Gateway) completeProjectDelete(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	if err := g.store.CompleteProjectDeletion(ctx, operation, *result); err != nil {
		return err
	}
	return publishOperationResult(ctx, g, serverID, *result)
}

func (g *Gateway) completeWorkspaceFiles(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.WorkspaceFilesCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if completed, err := g.store.CompleteWorkspaceFileScan(ctx, operation, command.WorkspaceID, *result); err != nil || !completed {
		return err
	}
	return publishOperationResult(ctx, g, serverID, *result)
}

func (g *Gateway) completeWorkspaceFileWrite(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.WorkspaceFileWriteCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if err := g.store.BeginWorkspaceFileScan(ctx, command.WorkspaceID); err != nil {
		return err
	}
	if _, err := g.store.QueueResourceOperation(ctx, serverID, protocol.OpWorkspaceFiles, protocol.WorkspaceFilesCommand{WorkspaceID: command.WorkspaceID, Path: command.Root}, operation.ID+":files", store.OperationResource{WorkspaceID: command.WorkspaceID}, false); err != nil {
		return err
	}
	if err := g.store.BeginWorkspaceChangeScan(ctx, command.WorkspaceID); err != nil {
		return err
	}
	if _, err := g.store.QueueResourceOperation(ctx, serverID, protocol.OpWorkspaceChanges, protocol.WorkspaceChangesCommand{WorkspaceID: command.WorkspaceID, Path: command.Root}, operation.ID+":changes", store.OperationResource{WorkspaceID: command.WorkspaceID}, false); err != nil {
		return err
	}
	g.Wake(serverID)

	return nil
}

func (g *Gateway) completeWorkspaceFilePreview(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.WorkspaceFilePreviewCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		var preview protocol.WorkspaceFilePreviewResult
		if err := json.Unmarshal(result.Data, &preview); err != nil {
			return err
		}
		if preview.Path != command.Path {
			return errors.New("workspace file preview path mismatch")
		}
		if err := g.store.SaveWorkspaceFilePreview(ctx, command.WorkspaceID, command.Path, preview); err != nil {
			return err
		}
	} else if err := g.store.FailWorkspaceFilePreview(ctx, command.WorkspaceID, command.Path, result.Message); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeWorkspaceChanges(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.WorkspaceChangesCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		var changes protocol.WorkspaceChangesResult
		if err := json.Unmarshal(result.Data, &changes); err != nil {
			return err
		}
		if err := g.store.SaveWorkspaceChanges(ctx, command.WorkspaceID, changes); err != nil {
			return err
		}
	} else if err := g.store.FailWorkspaceChangeScan(ctx, command.WorkspaceID, result.Message); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeWorkspaceDiffPreview(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.WorkspaceDiffCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		var diff protocol.WorkspaceDiffResult
		if err := json.Unmarshal(result.Data, &diff); err != nil {
			return err
		}
		if diff.Path != command.Path {
			return errors.New("workspace diff preview path mismatch")
		}
		if err := g.store.SaveWorkspaceDiffPreview(ctx, command.WorkspaceID, command.Path, diff); err != nil {
			return err
		}
	} else if err := g.store.FailWorkspaceDiffPreview(ctx, command.WorkspaceID, command.Path, result.Message); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeGitWorkspaceInspect(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.GitWorkspaceInspectCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		var snapshot protocol.GitWorkspaceInspectResult
		if err := json.Unmarshal(result.Data, &snapshot); err != nil {
			return err
		}
		if err := g.store.SaveWorkspaceGitSnapshot(ctx, command.WorkspaceID, snapshot); err != nil {
			return err
		}
	} else if err := g.store.FailWorkspaceGitRefresh(ctx, command.WorkspaceID, result.Message); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeGitWorkspaceWrite(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.GitWorkspaceWriteCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		var written protocol.GitWorkspaceWriteResult
		if err := json.Unmarshal(result.Data, &written); err != nil {
			return err
		}
		if err := g.store.SaveWorkspaceGitSnapshot(ctx, command.WorkspaceID, written.Snapshot); err != nil {
			return err
		}
	} else if err := g.store.FailWorkspaceGitRefresh(ctx, command.WorkspaceID, result.Message); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeCodexSnapshot(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.CodexSnapshotCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		result.Status, result.Message, result.Data = "failed", "Invalid Codex refresh request. Please refresh to retry.", nil
		command = protocol.CodexSnapshotCommand{}
		if operation.ThreadID != "" {
			command.ScopeType, command.ScopeID = "thread", operation.ThreadID
		} else if operation.WorkspaceID != "" {
			command.ScopeType, command.ScopeID = "workspace", operation.WorkspaceID
		}
	}
	if result.Status == "succeeded" {
		var snapshot *protocol.CodexCapabilityResult
		if err := json.Unmarshal(result.Data, &snapshot); err != nil || snapshot == nil {
			result.Status, result.Message, result.Data = "failed", "Invalid Codex refresh result. Please refresh to retry.", nil
		}
	} else {
		result.Status = "failed"
		if result.Message == "" {
			result.Message = "Codex information refresh failed. Please refresh to retry."
		}
	}
	if completed, err := g.store.CompleteCodexSnapshotOperation(ctx, operation, command, *result); err != nil || !completed {
		return err
	}
	return publishOperationResult(ctx, g, serverID, *result)
}

func (g *Gateway) completeCodexGoal(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.CodexSnapshotCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	kind := strings.TrimPrefix(operation.Kind, protocol.CodexKindPrefix)
	if strings.HasPrefix(kind, "goal.") {
		kind = "goal"
	}
	if result.Status == "succeeded" {
		var snapshot protocol.CodexCapabilityResult
		if err := json.Unmarshal(result.Data, &snapshot); err != nil {
			return err
		}
		if err := g.store.SaveCodexSnapshot(ctx, command.ScopeType, command.ScopeID, kind, snapshot); err != nil {
			return err
		}
	} else if err := g.store.FailCodexSnapshot(ctx, command.ScopeType, command.ScopeID, kind, result.Message); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeCodexThreadFork(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.ForkThreadCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	var fork protocol.ForkThreadResult
	if err := json.Unmarshal(result.Data, &fork); err != nil {
		return err
	}
	if err := store.ValidateThreadForkResult(command, fork); err != nil {
		result.Status, result.Message = "failed", err.Error()
	} else if err := g.store.CommitThreadFork(ctx, command, fork.CodexThread); err != nil {
		result.Status, result.Message = "failed", "could not persist fork: "+err.Error()
	}

	return nil
}

func (g *Gateway) completeClaudeThreadFork(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var err error

	var c protocol.ClaudeForkCommand
	var fork protocol.ClaudeForkResult
	if err = json.Unmarshal([]byte(operation.Payload), &c); err != nil {
		return err
	}
	if json.Unmarshal(result.Data, &fork) != nil {
		result.Status = "failed"
		result.Message = "invalid Claude fork result"
	} else if e := g.store.CommitClaudeFork(ctx, c, fork.SnapshotID); e != nil {
		result.Status = "failed"
		result.Message = "could not save Claude fork: " + e.Error()
	}

	return nil
}

func (g *Gateway) completeGeminiThreadFork(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var err error

	var command protocol.GeminiForkCommand
	var fork protocol.GeminiForkResult
	if err = json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if err = json.Unmarshal(result.Data, &fork); err != nil {
		result.Status = "failed"
		result.Message = "invalid Gemini branch result"
	} else if e := g.store.CommitGeminiFork(ctx, command, fork.SessionID); e != nil {
		result.Status = "failed"
		result.Message = "could not save Gemini branch: " + e.Error()
	}

	return nil
}

func (g *Gateway) completeGitWorktreeCreate(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	var command protocol.GitWorktreeCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	var worktree protocol.GitWorktreeCreateResult
	if err := json.Unmarshal(result.Data, &worktree); err != nil {
		return err
	}
	if commitErr := g.store.CommitGitWorktree(ctx, command, worktree); commitErr != nil {
		cleanup := protocol.GitWorktreeCleanupCommand{SourcePath: command.SourcePath, TargetPath: worktree.Path, Branch: worktree.Branch}
		if _, queueErr := g.store.QueueResourceOperation(ctx, serverID, protocol.OpGitWorktreeCleanup, cleanup, "git-worktree-cleanup:"+command.TargetWorkspaceID, store.OperationResource{ProjectID: operation.ProjectID, WorkspaceID: operation.WorkspaceID}, false); queueErr != nil {
			return fmt.Errorf("commit worktree: %v; queue cleanup: %w", commitErr, queueErr)
		}
		g.Wake(serverID)
		result.Status = "failed"
		result.Message = "could not persist worktree; cleanup queued: " + commitErr.Error()
	}

	return nil
}

func (g *Gateway) completeDeployContainer(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) error {
	if err := g.store.CompleteDeploymentContainerOperation(ctx, operation.ID, *result); err != nil {
		return err
	}

	return nil
}

func (g *Gateway) completeOperationResources(ctx context.Context, serverID string, operation store.Operation, result *protocol.OperationResult, outcomeUnknown bool) (bool, error) {
	if operation.Kind == protocol.OpGitWorkspaceClone {
		return true, g.completeGitWorkspaceClone(ctx, serverID, operation, result, outcomeUnknown)
	}

	if operation.Kind == protocol.OpGitWorkspaceLifecycle {
		return true, g.completeGitWorkspaceLifecycle(ctx, serverID, operation, result, outcomeUnknown)
	}

	// Blank project creation is special: the project and its primary
	// workspace must be committed together only after the agent result has
	// been checked. Do not let the generic completion path claim success
	// before that transaction finishes.
	if operation.Kind == protocol.OpGitProjectCreate {
		return true, g.completeGitProjectCreate(ctx, serverID, operation, result, outcomeUnknown)
	}

	if operation.Kind == store.ProjectDeleteOperationKind {
		return true, g.completeProjectDelete(ctx, serverID, operation, result, outcomeUnknown)
	}

	if operation.Kind == protocol.OpWorkspaceFiles {
		return true, g.completeWorkspaceFiles(ctx, serverID, operation, result, outcomeUnknown)
	}

	if operation.Kind == protocol.OpWorkspaceFileWrite && result.Status == "succeeded" {
		if err := g.completeWorkspaceFileWrite(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpWorkspaceFilePreview {
		if err := g.completeWorkspaceFilePreview(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpWorkspaceChanges {
		if err := g.completeWorkspaceChanges(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpWorkspaceDiffPreview {
		if err := g.completeWorkspaceDiffPreview(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpGitWorkspaceInspect {
		if err := g.completeGitWorkspaceInspect(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpGitWorkspaceWrite {
		if err := g.completeGitWorkspaceWrite(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if store.IsReadOnlyCodexSnapshotOperation(operation.Kind) {
		return true, g.completeCodexSnapshot(ctx, serverID, operation, result, outcomeUnknown)
	}

	if strings.HasPrefix(operation.Kind, protocol.OpCodexGoalPrefix) {
		if err := g.completeCodexGoal(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpCodexThreadFork && result.Status == "succeeded" {
		if err := g.completeCodexThreadFork(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpClaudeThreadFork && result.Status == "succeeded" {
		if err := g.completeClaudeThreadFork(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpGeminiThreadFork && result.Status == "succeeded" {
		if err := g.completeGeminiThreadFork(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpGitWorktreeCreate && result.Status == "succeeded" {
		if err := g.completeGitWorktreeCreate(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}

	if operation.Kind == protocol.OpDeployContainer || strings.HasPrefix(operation.Kind, protocol.OpDeployContainerPrefix) {
		if err := g.completeDeployContainer(ctx, serverID, operation, result, outcomeUnknown); err != nil {
			return true, err
		}
	}
	return false, nil
}

func (g *Gateway) completeClaudeForkSource(ctx context.Context, operation store.Operation) error {
	var err error

	var c protocol.ClaudeForkCommand
	if operation.Kind == protocol.OpClaudeThreadFork {
		_ = json.Unmarshal([]byte(operation.Payload), &c)
	} else {
		var w protocol.GitWorktreeCreateCommand
		_ = json.Unmarshal([]byte(operation.Payload), &w)
		if w.ClaudeFork != nil {
			c = *w.ClaudeFork
		}
	}
	if c.SourceThreadID != "" {
		if _, err = g.store.DB.ExecContext(ctx, g.store.Q("UPDATE claude_threads SET status='idle',updated_at=? WHERE id=? AND operation_id=?"), time.Now().UTC(), c.SourceThreadID, operation.ID); err != nil {
			return err
		}
		_ = g.publish(ctx, protocol.StreamEvent{StreamID: c.SourceThreadID, Kind: protocol.EventClaudeThreadUpdated, Payload: json.RawMessage(`{}`)})
	}

	return nil
}

func (g *Gateway) completeGeminiForkSource(ctx context.Context, operation store.Operation) error {
	var err error

	var command protocol.GeminiForkCommand
	if operation.Kind == protocol.OpGeminiThreadFork {
		_ = json.Unmarshal([]byte(operation.Payload), &command)
	} else {
		var w protocol.GitWorktreeCreateCommand
		_ = json.Unmarshal([]byte(operation.Payload), &w)
		if w.GeminiFork != nil {
			command = *w.GeminiFork
		}
	}
	if command.SourceThreadID != "" {
		if err = g.store.CompleteGeminiTurn(ctx, command.SourceThreadID, operation.ID, "idle"); err != nil {
			return err
		}
		_ = g.publish(ctx, protocol.StreamEvent{StreamID: command.SourceThreadID, Kind: protocol.EventGeminiThreadUpdated, Payload: json.RawMessage(`{}`)})
	}

	return nil
}
