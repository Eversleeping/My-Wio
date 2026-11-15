package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	"strings"
	"time"
)

// handleOperationResult applies an Agent result. outcomeUnknown marks results
// synthesized for operations the Agent lost track of: their side effects on
// disk may be partial, so resources are not reported as cleanly unchanged.
func (g *Gateway) handleOperationResult(ctx context.Context, serverID string, result protocol.OperationResult, outcomeUnknown bool) error {
	operation, err := g.store.Operation(ctx, result.OperationID)
	if err != nil {
		return err
	}
	if operation.ServerID != serverID {
		return errors.New("operation result server mismatch")
	}
	// A queued or in-flight operation may be cancelled from the UI. Ignore a
	// late Agent result so it cannot resurrect a cancelled turn or overwrite
	// its snapshot with stale data.
	if operation.Status != "queued" && operation.Status != "delivered" && operation.Status != "running" {
		return g.reconcileCompletedTurn(ctx, operation)
	}
	if result.Status == "cancelled" && protocol.CanInterruptOperation(operation.Kind) {
		completed, err := g.store.CompleteInterruptedOperation(ctx, operation, result)
		if err != nil || !completed {
			return err
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	geminiTurn := operation.Kind == protocol.OpGeminiTurnStart || operation.Kind == protocol.OpGeminiTurnRewrite || operation.Kind == protocol.OpClaudeTurnStart
	geminiInterrupted := geminiTurn && (result.Status == "interrupted" || result.Status == "cancelled" || (result.Status == "failed" && result.Message == context.Canceled.Error()))
	if geminiInterrupted {
		// Agent operations use the shared cancelled state for metrics and
		// filtering, while the Gemini session displays interrupted.
		result.Status = "cancelled"
	}
	resultLogFields := []any{
		"server_id", serverID,
		"operation_id", operation.ID,
		"kind", operation.Kind,
		"status", result.Status,
		"total_elapsed_ms", time.Since(operation.CreatedAt).Milliseconds(),
	}
	if operation.DeliveredAt != nil {
		resultLogFields = append(resultLogFields, "execution_elapsed_ms", time.Since(*operation.DeliveredAt).Milliseconds())
	}
	g.evaluateOperationLatency(ctx, serverID, operation)
	g.log.Info("Agent operation result received", resultLogFields...)
	if operation.Kind == protocol.OpGitWorkspaceClone {
		var command protocol.GitWorkspaceCloneCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		if result.Status != "succeeded" {
			if result.Message == "" {
				result.Message = "agent failed to clone workspace"
			}
			if err := g.store.FailCrossServerWorkspaceClone(ctx, operation, result, uncertainStatus(outcomeUnknown, "ready")); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		var cloned protocol.GitWorkspaceCloneResult
		if err := json.Unmarshal(result.Data, &cloned); err != nil {
			result.Status = "failed"
			result.Message = "invalid workspace clone result: " + err.Error()
			if err := g.store.FailCrossServerWorkspaceClone(ctx, operation, result, "partial"); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		if err := g.store.CompleteCrossServerWorkspaceClone(ctx, operation, command, cloned, result); err != nil {
			result.Status = "failed"
			result.Message = "workspace cloned but control-plane commit failed: " + err.Error()
			if partialErr := g.store.FailCrossServerWorkspaceClone(ctx, operation, result, "partial"); partialErr != nil {
				return fmt.Errorf("commit workspace clone: %v; mark partial: %w", err, partialErr)
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	if operation.Kind == protocol.OpGitWorkspaceLifecycle {
		var command protocol.GitWorkspaceLifecycleCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		if result.Status != "succeeded" {
			if result.Message == "" {
				result.Message = "agent failed to update workspace"
			}
			if err := g.store.FailWorkspaceLifecycle(ctx, operation, command, result, uncertainStatus(outcomeUnknown, "ready")); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		var lifecycleResult protocol.GitWorkspaceLifecycleResult
		if err := json.Unmarshal(result.Data, &lifecycleResult); err != nil {
			result.Status = "failed"
			result.Message = "invalid workspace lifecycle result: " + err.Error()
			if err := g.store.FailWorkspaceLifecycle(ctx, operation, command, result, "partial"); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		if err := g.store.CompleteWorkspaceLifecycle(ctx, operation, command, lifecycleResult, result); err != nil {
			result.Status = "failed"
			result.Message = "workspace files changed but control-plane commit failed: " + err.Error()
			if partialErr := g.store.FailWorkspaceLifecycle(ctx, operation, command, result, "partial"); partialErr != nil {
				return fmt.Errorf("commit workspace lifecycle: %v; mark partial: %w", err, partialErr)
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	// Blank project creation is special: the project and its primary
	// workspace must be committed together only after the agent result has
	// been checked. Do not let the generic completion path claim success
	// before that transaction finishes.
	if operation.Kind == protocol.OpGitProjectCreate {
		var command protocol.GitProjectCreateCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		if result.Status != "succeeded" {
			if result.Message == "" {
				result.Message = "agent failed to create blank project"
			}
			if err := g.store.FailBlankProject(ctx, operation, command, result, uncertainStatus(outcomeUnknown, "failed")); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		var created protocol.GitProjectCreateResult
		if err := json.Unmarshal(result.Data, &created); err != nil {
			result.Status = "failed"
			result.Message = "invalid blank project result: " + err.Error()
			if err := g.store.FailBlankProject(ctx, operation, command, result, "failed"); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		if err := store.ValidateBlankProjectResult(command, created); err != nil {
			result.Status = "failed"
			result.Message = err.Error()
			if err := g.store.FailBlankProject(ctx, operation, command, result, "failed"); err != nil {
				return err
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		if err := g.store.CommitBlankProject(ctx, operation, command, created, result); err != nil {
			// The agent has already created the directory. Preserve it and
			// leave a retryable partial project when the DB transaction fails.
			result.Status = "failed"
			result.Message = "project created on server but could not persist workspace: " + err.Error()
			if partialErr := g.store.FailBlankProject(ctx, operation, command, result, "partial"); partialErr != nil {
				return fmt.Errorf("persist blank project: %v; mark partial: %w", err, partialErr)
			}
			return publishOperationResult(ctx, g, serverID, result)
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	if operation.Kind == store.ProjectDeleteOperationKind {
		if err := g.store.CompleteProjectDeletion(ctx, operation, result); err != nil {
			return err
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	if operation.Kind == protocol.OpWorkspaceFiles {
		var command protocol.WorkspaceFilesCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		if completed, err := g.store.CompleteWorkspaceFileScan(ctx, operation, command.WorkspaceID, result); err != nil || !completed {
			return err
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	if operation.Kind == protocol.OpWorkspaceFileWrite && result.Status == "succeeded" {
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
	}
	if operation.Kind == protocol.OpWorkspaceFilePreview {
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
	}
	if operation.Kind == protocol.OpWorkspaceChanges {
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
	}
	if operation.Kind == protocol.OpWorkspaceDiffPreview {
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
	}
	if operation.Kind == protocol.OpGitWorkspaceInspect {
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
	}
	if operation.Kind == protocol.OpGitWorkspaceWrite {
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
	}
	if store.IsReadOnlyCodexSnapshotOperation(operation.Kind) {
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
		if completed, err := g.store.CompleteCodexSnapshotOperation(ctx, operation, command, result); err != nil || !completed {
			return err
		}
		return publishOperationResult(ctx, g, serverID, result)
	}
	if strings.HasPrefix(operation.Kind, protocol.OpCodexGoalPrefix) {
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
	}
	if operation.Kind == protocol.OpCodexThreadFork && result.Status == "succeeded" {
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
	}
	if operation.Kind == protocol.OpClaudeThreadFork && result.Status == "succeeded" {
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
	}
	if operation.Kind == protocol.OpGeminiThreadFork && result.Status == "succeeded" {
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
	}
	if operation.Kind == protocol.OpGitWorktreeCreate && result.Status == "succeeded" {
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
	}
	if operation.Kind == protocol.OpDeployContainer || strings.HasPrefix(operation.Kind, protocol.OpDeployContainerPrefix) {
		if err := g.store.CompleteDeploymentContainerOperation(ctx, operation.ID, result); err != nil {
			return err
		}
	}
	geminiCompletionClaimed := false
	if operation.Kind == protocol.OpGeminiCredentialsConfigure {
		err = g.store.CompleteGeminiCredentialUpdate(ctx, result)
	} else if operation.Kind == protocol.OpClaudeCredentialsConfigure {
		err = g.store.CompleteClaudeCredentialUpdate(ctx, result)
	} else if operation.Kind == protocol.OpCredentialsConfigure || operation.Kind == protocol.OpCodexCredentialsConfigure || operation.Kind == protocol.OpGitCredentialsConfigure {
		err = g.store.CompleteCredentialUpdate(ctx, result)
	} else if geminiInterrupted || operation.Kind == protocol.OpClaudeTurnStart {
		geminiCompletionClaimed, err = g.store.CompleteOperationIfActive(ctx, result)
	} else {
		err = g.store.CompleteOperation(ctx, result)
	}
	if err != nil {
		return err
	}
	if (geminiInterrupted || operation.Kind == protocol.OpClaudeTurnStart) && !geminiCompletionClaimed {
		return nil
	}
	if operation.Kind == protocol.OpClaudeThreadFork || operation.Kind == protocol.OpGitWorktreeCreate {
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
	}
	if operation.Kind == protocol.OpGeminiThreadFork || operation.Kind == protocol.OpGitWorktreeCreate {
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
	}
	updatedOperation, statusErr := g.store.Operation(ctx, result.OperationID)
	if statusErr != nil {
		return statusErr
	}
	if updatedOperation.Status == "cancelled" && !geminiCompletionClaimed {
		return nil
	}
	if operation.Kind == protocol.OpGeminiTurnStart || operation.Kind == protocol.OpGeminiTurnRewrite || operation.Kind == protocol.OpGeminiTurnInterrupt || operation.Kind == protocol.OpGeminiTurnSteer {
		if err := g.completeGeminiOperation(ctx, operation, result); err != nil {
			return err
		}
	}
	if operation.Kind == protocol.OpClaudeTurnStart || operation.Kind == protocol.OpClaudeTurnInterrupt || operation.Kind == protocol.OpClaudeTurnSteer || operation.Kind == protocol.OpClaudeQuestionAnswer {
		if err := g.completeClaudeOperation(ctx, operation, result); err != nil {
			return err
		}
	}
	if operation.Kind == protocol.OpCodexTurnStart || operation.Kind == protocol.OpCodexTurnRewrite {
		if result.Status == "succeeded" {
			_ = g.store.MarkScheduledTaskByOperation(ctx, result.OperationID, "running", "")
		}
	}
	if (operation.Kind == protocol.OpCodexTurnStart || operation.Kind == protocol.OpCodexTurnRewrite) && result.Status == "failed" {
		if err := g.failCodexTurn(ctx, operation, result); err != nil {
			return err
		}
	}
	if (operation.Kind == protocol.OpCodexTurnInterrupt || operation.Kind == protocol.OpCodexTurnSteer || operation.Kind == protocol.OpCodexApproval || operation.Kind == protocol.OpCodexThreadCompact) && result.Status == "failed" {
		if err := g.failCodexControlOperation(ctx, operation, result); err != nil {
			return err
		}
	}
	// Image bytes are fetched through the authenticated preview APIs. Broadcasting
	// them to every connected console duplicates multi-megabyte payloads.
	eventResult := result
	if operation.Kind == protocol.OpWorkspaceImagePreview || operation.Kind == protocol.OpWorkspaceFilePreview {
		eventResult.Data = nil
	}
	payload, err := json.Marshal(eventResult)
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: protocol.EventOperationPrefix + result.Status, Payload: security.RedactJSON(payload)})
}

// A completion can commit its operation before a later session update fails.
// Repair only the still-bound active turn, using the stored result rather than
// the late Agent payload. In particular, a UI-cancelled session stays stopped.
func (g *Gateway) reconcileCompletedTurn(ctx context.Context, operation store.Operation) error {
	if operation.Status != "succeeded" && operation.Status != "failed" && operation.Status != "cancelled" {
		return nil
	}
	engine := ""
	switch operation.Kind {
	case protocol.OpClaudeTurnInterrupt, protocol.OpGeminiTurnInterrupt:
		if operation.Status != "succeeded" {
			return nil
		}
		result := protocol.OperationResult{OperationID: operation.ID, Status: operation.Status, Message: operation.Result, Data: json.RawMessage(operation.ResultData)}
		if operation.Kind == protocol.OpClaudeTurnInterrupt {
			return g.completeClaudeOperation(ctx, operation, result)
		}
		return g.completeGeminiOperation(ctx, operation, result)
	case protocol.OpClaudeTurnStart:
		engine = "claude"
	case protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite:
		engine = "gemini"
	case protocol.OpCodexTurnStart, protocol.OpCodexTurnRewrite:
		if operation.Status != "failed" {
			return nil // A successful Codex start only acknowledges acceptance.
		}
		engine = "codex"
	default:
		return nil
	}
	var command struct {
		ThreadID string `json:"thread_id"`
		Start    struct {
			ThreadID string `json:"thread_id"`
		} `json:"start"`
	}
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	threadID := command.ThreadID
	if threadID == "" {
		threadID = command.Start.ThreadID
	}
	var active int
	query := "SELECT COUNT(*) FROM " + engine + "_threads WHERE id=? AND status IN ('queued','running','interrupting')"
	args := []any{threadID}
	if engine != "codex" {
		query += " AND operation_id=?"
		args = append(args, operation.ID)
	}
	if err := g.store.DB.GetContext(ctx, &active, g.store.Q(query), args...); err != nil {
		return err
	}
	if active == 0 {
		return nil
	}
	result := protocol.OperationResult{OperationID: operation.ID, Status: operation.Status, Message: operation.Result, Data: json.RawMessage(operation.ResultData)}
	switch engine {
	case "claude":
		return g.completeClaudeOperation(ctx, operation, result)
	case "gemini":
		return g.completeGeminiOperation(ctx, operation, result)
	default:
		return g.failCodexTurn(ctx, operation, result)
	}
}

// A lost operation may have stopped half-way through moving, cloning or
// creating files; keep the resource retryable instead of claiming it is clean.
func uncertainStatus(outcomeUnknown bool, status string) string {
	if outcomeUnknown {
		return "partial"
	}
	return status
}

// reconcileOperation handles the Agent's answer to a keepalive reconcile
// request. Only an explicit "unknown" fails an operation: old Agents never
// answer, and an active long-running deploy or turn answers "active".
func (g *Gateway) reconcileOperation(ctx context.Context, serverID string, reconcile protocol.OperationReconcile) error {
	if reconcile.State != protocol.OperationReconcileUnknown {
		return nil
	}
	operation, err := g.store.Operation(ctx, reconcile.OperationID)
	if err != nil {
		return err
	}
	if operation.ServerID != serverID {
		return errors.New("operation reconcile server mismatch")
	}
	// Only running operations were asked about; a queued or delivered row is
	// being re-sent and a finished one already has its result.
	if operation.Status != "running" || !store.ReconcilesOperationKind(operation.Kind) {
		return nil
	}
	g.log.Warn("Agent lost track of running operation", "server_id", serverID, "operation_id", operation.ID, "kind", operation.Kind)
	if err := g.failLostDeployment(ctx, operation); err != nil {
		return err
	}
	return g.handleOperationResult(ctx, serverID, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: protocol.OperationLostMessage}, true)
}

func publishOperationResult(ctx context.Context, g *Gateway, serverID string, result protocol.OperationResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: protocol.EventOperationPrefix + result.Status, Payload: security.RedactJSON(payload)})
}
