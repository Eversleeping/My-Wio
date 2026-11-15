package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/gitworktree"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type operationExecution struct {
	kind        string
	cancel      context.CancelFunc
	done        chan struct{}
	result      protocol.OperationResult
	completedAt time.Time
	persisted   bool
}

func operationContext(parent context.Context, kind string) (context.Context, context.CancelFunc) {
	// Gemini turns run synchronously until inference and all tools finish, and
	// can also wait for user input. Their lifetime follows explicit interruption
	// or Agent shutdown, rather than the maintenance-operation deadline.
	if kind == protocol.OpGeminiTurnStart || kind == protocol.OpGeminiTurnRewrite || kind == protocol.OpClaudeTurnStart {
		return context.WithCancel(parent)
	}
	timeout := 10 * time.Minute
	if strings.HasPrefix(kind, protocol.OpDeployPrefix) {
		timeout = time.Hour
	} else if kind == protocol.OpAgentUpdate || kind == protocol.OpCodexUpdate {
		timeout = 30 * time.Minute
	}
	return context.WithTimeout(parent, timeout)
}

func (c *Client) handleOperation(parent context.Context, envelope *protocol.ControlEnvelope) {
	if envelope.OperationID == "" {
		return
	}
	c.seenMu.Lock()
	c.pruneCompletedOperations(time.Now())
	execution, seen := c.seen[envelope.OperationID]
	if seen {
		done := execution.done
		c.seenMu.Unlock()
		select {
		case <-done:
		default:
			// The control plane re-sends a delivered operation only when its start
			// notice never arrived; repeat it so the re-sends stop.
			_ = c.queue(protocol.AgentKindOperationStarted, protocol.OperationStarted{OperationID: envelope.OperationID}, true)
		}
		select {
		case <-done:
			c.seenMu.Lock()
			result := execution.result
			c.seenMu.Unlock()
			c.replayOperationResult(parent, envelope, result)
		case <-parent.Done():
		}
		return
	}
	execution = &operationExecution{kind: envelope.Kind, done: make(chan struct{})}
	if result, found, err := c.loadOperationResult(envelope.OperationID); found || err != nil {
		c.seenMu.Unlock()
		if err != nil {
			result = protocol.OperationResult{OperationID: envelope.OperationID, Status: "failed", Message: "could not read previous operation result; refusing to repeat operation"}
		}
		c.replayOperationResult(parent, envelope, result)
		return
	}
	ctx, cancel := operationContext(parent, envelope.Kind)
	defer cancel()
	execution.cancel = cancel
	c.seen[envelope.OperationID] = execution
	// Checked under seenMu, like stageAgentUpdate's scan of active executions,
	// so an operation cannot slip in between staging and exec and be wiped.
	blockedByUpdate := c.updatePending && envelope.Kind != protocol.OpAgentUpdate && !protocol.SafeDuringAgentRestart(envelope.Kind)
	c.seenMu.Unlock()
	if err := c.queue(protocol.AgentKindOperationStarted, protocol.OperationStarted{OperationID: envelope.OperationID}, true); err != nil {
		c.log.Warn("could not queue operation start", "operation_id", envelope.OperationID, "error", err)
	}
	var restartPath string
	var resultData json.RawMessage
	refreshInventory := false
	var err error
	if blockedByUpdate {
		err = errors.New("Agent update is in progress; retry the task after it reconnects")
	} else if envelope.Kind == protocol.OpOperationInterrupt {
		var command protocol.OperationInterruptCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
			err = c.interruptOperation(stopContext, command)
			stopCancel()
		}
	} else if envelope.Kind == protocol.OpAgentUpdate {
		var command protocol.AgentUpdateCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			restartPath, err = c.stageAgentUpdate(ctx, command)
		}
	} else if envelope.Kind == protocol.OpGeminiTurnInterrupt {
		var command protocol.GeminiInterruptCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
			err = c.interruptGeminiAndWait(stopContext, command.OperationID)
			stopCancel()
			if err == nil {
				resultData, err = json.Marshal(protocol.GeminiInterruptResult{OperationID: command.OperationID, Stopped: true})
			}
		}
	} else if envelope.Kind == protocol.OpClaudeTurnSteer {
		var command protocol.ClaudeSteerCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			err = c.claudeTurns.Steer(ctx, command)
		}
	} else if envelope.Kind == protocol.OpClaudeTurnInterrupt {
		var command protocol.ClaudeInterruptCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
			err = c.interruptClaudeAndWait(stopContext, command.OperationID)
			stopCancel()
			if err == nil {
				resultData, err = json.Marshal(protocol.GeminiInterruptResult{OperationID: command.OperationID, Stopped: true})
			}
		}
	} else if envelope.Kind == protocol.OpGeminiSkillsManage {
		var command protocol.GeminiSkillManageCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			if c.agentUpdatePending() {
				err = errors.New("Agent update is in progress")
			} else {
				command.Workspace, err = scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
				if err == nil {
					var result protocol.GeminiSkillsResult
					result, err = c.gemini.ManageSkill(ctx, command)
					if err == nil {
						resultData, err = json.Marshal(result)
					}
				}
			}
		}
	} else if envelope.Kind == protocol.OpClaudeSkillsList {
		var command protocol.ClaudeSkillsCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.ClaudeSkillsResult
			result, err = c.listClaudeSkills(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpGeminiSkillsList {
		var command protocol.GeminiSkillsCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var root string
			root, err = scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
			if err == nil {
				var result protocol.GeminiSkillsResult
				result, err = c.gemini.ListSkills(ctx, envelope.OperationID, root)
				if err == nil {
					resultData, err = json.Marshal(result)
				}
			}
		}
	} else if envelope.Kind == protocol.OpWorkspaceFiles {
		var command protocol.WorkspaceFilesCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.WorkspaceFilesResult
			result, err = scanner.ListWorkspaceFiles(ctx, command.Path, c.inventoryRoots())
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpWorkspaceFileWrite {
		var command protocol.WorkspaceFileWriteCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			err = scanner.WriteWorkspaceFile(ctx, command, c.inventoryRoots())
		}
	} else if envelope.Kind == protocol.OpWorkspaceFilePreview || envelope.Kind == protocol.OpWorkspaceImagePreview {
		var command protocol.WorkspaceFilePreviewCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.WorkspaceFilePreviewResult
			result, err = scanner.ReadWorkspaceFile(ctx, command.Root, command.Path, c.inventoryRoots(), 1024*1024)
			if err == nil && envelope.Kind == protocol.OpWorkspaceImagePreview && result.Encoding != "base64" {
				err = errors.New("image preview only supports JPEG, PNG, GIF and WebP files")
			}
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpWorkspaceChanges {
		var command protocol.WorkspaceChangesCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var changes []gitrepository.WorkspaceChange
			changes, err = gitrepository.ListWorkspaceChanges(ctx, command.Path, c.inventoryRoots())
			if err == nil {
				result := protocol.WorkspaceChangesResult{Changes: make([]protocol.WorkspaceChange, 0, len(changes))}
				for _, change := range changes {
					result.Changes = append(result.Changes, protocol.WorkspaceChange{Path: change.Path, OldPath: change.OldPath, Status: change.Status, Staged: change.Staged, Unstaged: change.Unstaged})
				}
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpWorkspaceDiffPreview {
		var command protocol.WorkspaceDiffCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var diff gitrepository.WorkspaceDiff
			diff, err = gitrepository.ReadWorkspaceDiff(ctx, command.Root, command.Path, command.OldPath, c.inventoryRoots(), gitrepository.MaxWorkspaceDiffBytes)
			if err == nil {
				result := protocol.WorkspaceDiffResult{Path: diff.Path, Content: diff.Content, Additions: diff.Additions, Deletions: diff.Deletions, Binary: diff.Binary, Truncated: diff.Truncated}
				resultData, err = json.Marshal(result)
			}
		}
	} else if strings.HasPrefix(envelope.Kind, protocol.OpCodexGoalPrefix) || envelope.Kind == protocol.OpCodexMCPList || envelope.Kind == protocol.OpCodexSkillsList || envelope.Kind == protocol.OpCodexModelsList || envelope.Kind == protocol.OpCodexStatusSnapshot || envelope.Kind == protocol.OpCodexThreadCompact {
		var result protocol.CodexCapabilityResult
		result, err = c.codex.CodexOperation(ctx, envelope.Kind, envelope.PayloadJSON)
		if err == nil && envelope.Kind == protocol.OpCodexThreadCompact && !result.Supported {
			err = errors.New(result.Reason)
		}
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	} else if envelope.Kind == protocol.OpCodexThreadFork {
		var command protocol.ForkThreadCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.ForkThreadResult
			result, err = c.codex.ForkThread(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpGitProjectCreate {
		var command protocol.GitProjectCreateCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitProjectCreateResult
			result, err = c.createProject(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
				refreshInventory = err == nil
			}
		}
	} else if envelope.Kind == protocol.OpGitProjectDelete {
		var command protocol.GitProjectDeleteCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitProjectDeleteResult
			result, err = c.deleteProject(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
				refreshInventory = err == nil
			}
		}
	} else if envelope.Kind == protocol.OpGitWorkspaceInspect {
		var command protocol.GitWorkspaceInspectCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorkspaceInspectResult
			result, err = c.inspectWorkspaceGit(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpGitWorkspaceWrite {
		var command protocol.GitWorkspaceWriteCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorkspaceWriteResult
			result, err = c.writeWorkspaceGit(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpGitWorkspaceLifecycle {
		var command protocol.GitWorkspaceLifecycleCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorkspaceLifecycleResult
			result.WorkspaceID, result.TargetWorkspaceID, result.Action, result.SourcePath = command.WorkspaceID, command.TargetWorkspaceID, command.Action, command.SourcePath
			switch command.Action {
			case "move":
				_, err = gitrepository.MoveWorkspace(ctx, command.SourcePath, command.TargetPath, c.managedRoots())
			case "copy":
				_, err = gitrepository.CopyWorkspace(ctx, command.SourcePath, command.TargetPath, c.managedRoots())
			case "delete":
				err = gitrepository.DeleteWorkspace(ctx, command.SourcePath, command.Force, c.managedRoots())
			default:
				err = fmt.Errorf("unsupported workspace lifecycle action %q", command.Action)
			}
			if err == nil {
				if command.Action != "delete" {
					result.TargetPath = filepath.Clean(command.TargetPath)
				}
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpGitWorkspaceClone {
		var command protocol.GitWorkspaceCloneCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			destination, destinationErr := projectCreateDestination(c.config.CloneRoot, command.Name, command.Destination)
			if destinationErr != nil {
				err = destinationErr
			} else {
				var cloned gitrepository.CloneWorkspaceResult
				cloned, err = gitrepository.CloneWorkspace(ctx, gitrepository.CloneWorkspaceOptions{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: destination, RemoteURL: command.RemoteURL, Branch: command.Branch, ExpectedHead: command.ExpectedHead}, c.managedRoots())
				if err == nil {
					resultData, err = json.Marshal(protocol.GitWorkspaceCloneResult{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: filepath.Clean(command.Destination), Branch: cloned.Branch, CommitSHA: cloned.CommitSHA})
				}
			}
		}
	} else if envelope.Kind == protocol.OpClaudeThreadFork {
		var command protocol.ClaudeForkCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			_, err = scanner.ResolveWorkspaceRoot(command.SourceWorkspace, c.inventoryRoots())
			if err == nil {
				var fork protocol.ClaudeForkResult
				fork, err = claudeadapter.Snapshot(ctx, c.config.StateDir, command)
				if err == nil {
					resultData, err = json.Marshal(fork)
				}
			}
		}
	} else if envelope.Kind == protocol.OpGeminiThreadFork {
		var command protocol.GeminiForkCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var root string
			root, err = scanner.ResolveWorkspaceRoot(command.SourceWorkspace, c.inventoryRoots())
			if err == nil && root != command.SourceWorkspace {
				err = errors.New("Gemini source path changed")
			}
			if err == nil {
				_, err = scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
			}
			if err == nil {
				var fork protocol.GeminiForkResult
				fork, err = c.gemini.Fork(ctx, envelope.OperationID, command)
				if err == nil {
					resultData, err = json.Marshal(fork)
				}
			}
		}
	} else if envelope.Kind == protocol.OpGitWorktreeCreate {
		var command protocol.GitWorktreeCreateCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorktreeCreateResult
			result, err = gitworktree.Create(ctx, command, c.inventoryRoots())
			if err == nil && command.ClaudeFork != nil {
				if command.ClaudeFork.SourceWorkspace != command.SourcePath || command.ClaudeFork.Workspace != result.Path {
					err = errors.New("invalid Claude worktree paths")
				} else {
					var fork protocol.ClaudeForkResult
					fork, err = claudeadapter.Snapshot(ctx, c.config.StateDir, *command.ClaudeFork)
					result.ClaudeSnapshotID = fork.SnapshotID
				}
				if err != nil {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					cleanupErr := gitworktree.Remove(cleanupCtx, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
					cancel()
					if cleanupErr != nil {
						err = fmt.Errorf("Claude fork failed: %v; cleanup: %w", err, cleanupErr)
					}
				}
			}
			if err == nil && command.GeminiFork != nil {
				sourceRoot, rootErr := scanner.ResolveWorkspaceRoot(command.SourcePath, c.inventoryRoots())
				if rootErr != nil || sourceRoot != command.GeminiFork.SourceWorkspace || command.GeminiFork.Workspace != result.Path {
					err = errors.New("invalid Gemini worktree paths")
				} else {
					var fork protocol.GeminiForkResult
					fork, err = c.gemini.Fork(ctx, envelope.OperationID, *command.GeminiFork)
					result.GeminiSessionID = fork.SessionID
				}
				if err != nil {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					cleanupErr := gitworktree.Remove(cleanupCtx, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
					cancel()
					if cleanupErr != nil {
						err = fmt.Errorf("Gemini fork failed: %v; worktree cleanup: %w", err, cleanupErr)
					}
				}
			}
			if err == nil && command.TargetThreadID != "" {
				forkCommand := protocol.ForkThreadCommand{SourceThreadID: command.SourceThreadID, TargetThreadID: command.TargetThreadID, CodexThread: command.CodexThread, WorkspaceID: command.TargetWorkspaceID, Workspace: result.Path, Title: command.Title, LastTurnID: command.LastTurnID, CutoffSequence: command.CutoffSequence}
				var fork protocol.ForkThreadResult
				fork, err = c.codex.ForkThread(ctx, forkCommand)
				if err != nil {
					cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
					cleanupErr := gitworktree.Remove(cleanupContext, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
					cancelCleanup()
					if cleanupErr != nil {
						err = fmt.Errorf("fork Codex thread: %v; cleanup worktree: %w", err, cleanupErr)
					}
				} else {
					result.CodexThread = fork.CodexThread
					result.LastTurnID = fork.LastTurnID
				}
			}
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == protocol.OpGitWorktreeCleanup {
		var command protocol.GitWorktreeCleanupCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			err = gitworktree.Remove(ctx, command.SourcePath, command.TargetPath, command.Branch, c.inventoryRoots())
		}
	} else if envelope.Kind == protocol.OpDeployContainer || strings.HasPrefix(envelope.Kind, protocol.OpDeployContainerPrefix) {
		var command protocol.ContainerActionCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			if command.Action == "" {
				command.Action = strings.TrimPrefix(envelope.Kind, protocol.OpDeployContainerPrefix)
			}
			var actionResult protocol.ContainerActionResult
			actionResult, err = c.runContainerAction(ctx, command)
			if marshaled, marshalErr := json.Marshal(actionResult); marshalErr != nil {
				err = marshalErr
			} else {
				resultData = marshaled
			}
		}
	} else {
		err = c.execute(ctx, envelope)
	}
	status := "succeeded"
	message := ""
	if err != nil {
		status = "failed"
		if protocol.CanInterruptOperation(envelope.Kind) && errors.Is(ctx.Err(), context.Canceled) {
			status = "cancelled"
		}
		if (envelope.Kind == protocol.OpGeminiTurnStart || envelope.Kind == protocol.OpGeminiTurnRewrite || envelope.Kind == protocol.OpClaudeTurnStart) && (errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)) {
			status = "interrupted"
		}
		message = err.Error()
		c.log.Warn("operation failed", "operation_id", envelope.OperationID, "kind", envelope.Kind, "error", err)
	}
	result := limitOperationResult(protocol.OperationResult{OperationID: envelope.OperationID, Status: status, Message: truncate(message, 8192), Data: resultData})
	if result.Status != status {
		c.log.Warn("operation result exceeds the Agent message limit", "operation_id", envelope.OperationID, "kind", envelope.Kind, "bytes", len(resultData))
	}
	resultSaved := c.config.StateDir == ""
	if err := c.saveOperationResult(result); err != nil {
		c.log.Warn("could not persist operation result", "operation_id", envelope.OperationID, "error", err)
	} else {
		resultSaved = true
	}
	c.seenMu.Lock()
	execution.result = result
	execution.completedAt = time.Now()
	execution.persisted = resultSaved
	close(execution.done)
	c.pruneCompletedOperations(execution.completedAt)
	c.seenMu.Unlock()
	var resultSent <-chan struct{}
	var queueErr error
	if restartPath != "" {
		resultSent, queueErr = c.queueTracked(protocol.AgentKindOperationResult, result)
	} else {
		queueErr = c.queue(protocol.AgentKindOperationResult, result, true)
	}
	if queueErr != nil {
		c.log.Warn("could not queue operation result", "operation_id", envelope.OperationID, "error", queueErr)
	}
	if refreshInventory {
		c.startWorker(func() { c.refreshInventory(parent) })
	}
	if err == nil && restartPath != "" {
		// The receipt is already persisted, so a result lost here is replayed by
		// the next process' reconcile; waiting is still the common fast path.
		if !waitForSent(resultSent, agentUpdateFlushTimeout) {
			c.log.Warn("Agent update result was not sent before restart; it will be replayed after reconnect", "operation_id", envelope.OperationID)
		}
		if restartErr := activateStagedUpdate(c.config.StateDir, restartPath); restartErr != nil {
			c.releaseAgentUpdate()
			c.log.Error("could not restart into Agent update", "operation_id", envelope.OperationID, "error", restartErr)
			failure := protocol.OperationResult{OperationID: envelope.OperationID, Status: "failed", Message: truncate(restartErr.Error(), 8192)}
			_ = c.saveOperationResult(failure)
			c.seenMu.Lock()
			execution.result = failure
			c.seenMu.Unlock()
			_ = c.queue(protocol.AgentKindOperationResult, failure, true)
		}
	}
}

func (c *Client) execute(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	switch envelope.Kind {
	case protocol.OpInventoryScan:
		return c.enqueueInventory(ctx)
	case protocol.OpGitImport:
		var command protocol.GitImportCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		if _, err := scanner.Import(ctx, c.config.CloneRoot, command); err != nil {
			return err
		}
		return c.enqueueInventory(ctx)
	case protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite:
		if c.agentUpdatePending() {
			return errors.New("Agent update is in progress; retry the task after it reconnects")
		}
		var command protocol.GeminiTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		root, err := scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
		if err != nil {
			return err
		}
		command.Workspace = root
		environment, err := c.geminiGitEnvironment(ctx)
		if err != nil {
			return err
		}
		bridge, closeBridge, bridgeErr := c.sessionBridge(ctx, sessiontools.Ref{Engine: "gemini", ID: command.ThreadID}, envelope.OperationID)
		if bridgeErr == nil {
			command.CollaborationMCP = bridge
			defer closeBridge()
		} else {
			c.log.Warn("collaboration bridge unavailable", "engine", "gemini")
		}
		return c.gemini.RunInTrustedWorkspaceWithEnvironment(ctx, envelope.OperationID, command, environment)
	case protocol.OpClaudeTurnStart:
		if c.agentUpdatePending() {
			return errors.New("Agent update is in progress; retry the task after it reconnects")
		}
		var command protocol.ClaudeTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		root, err := scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
		if err != nil {
			return err
		}
		command.Workspace = root
		if !c.claudeMu.TryRLock() {
			return errors.New("Claude configuration or installation is in progress")
		}
		defer c.claudeMu.RUnlock()
		if command.Rewrite != nil {
			if err := claudeadapter.PrepareRewrite(ctx, c.config.StateDir, envelope.OperationID, &command); err != nil {
				return err
			}
			defer func() {
				_ = claudeadapter.RemoveSnapshot(c.config.StateDir, envelope.OperationID)
			}()
		}
		if command.ForkSnapshotID != "" && command.SessionID == "" {
			if command.ForkSnapshotID != command.ThreadID {
				return errors.New("Claude snapshot does not match thread")
			}
			command.ResumePath, err = claudeadapter.SnapshotPath(c.config.StateDir, command.ForkSnapshotID)
			if err != nil {
				return err
			}
		}
		environment, err := c.claudeEnvironment()
		if err != nil {
			return err
		}
		environment, stopStreamProxy := c.claudeStreamProxy(environment)
		defer stopStreamProxy()
		gitEnvironment, err := c.geminiGitEnvironment(ctx)
		if err != nil {
			return err
		}
		environment = append(environment, gitEnvironment...)
		cliVersion := c.currentClaudeVersion(ctx, effectiveClaudePath(c.config.StateDir))
		command.Questions = command.Questions && buildinfo.SupportsClaudeQuestions(buildinfo.Version, cliVersion)
		command.Steer = command.Steer && buildinfo.SupportsClaudeSteering(buildinfo.Version, cliVersion)
		bridge, closeBridge, bridgeErr := c.sessionBridge(ctx, sessiontools.Ref{Engine: "claude", ID: command.ThreadID}, envelope.OperationID)
		if bridgeErr == nil {
			command.CollaborationMCP = bridge
			defer closeBridge()
		} else {
			c.log.Warn("collaboration bridge unavailable", "engine", "claude")
		}
		forkSnapshotID := command.ForkSnapshotID
		var observedInit bool
		err = c.claudeTurns.Run(ctx, effectiveClaudePath(c.config.StateDir), envelope.OperationID, command, func(event protocol.StreamEvent) error {
			if event.EventID == "" {
				event.EventID = uuid.NewString()
			}
			if event.Kind == protocol.EventClaudeInit {
				observedInit = true
			}
			return c.queue(protocol.AgentKindClaudeEvent, event, true)
		}, environment...)
		if err == nil && observedInit && forkSnapshotID != "" {
			_ = claudeadapter.RemoveSnapshot(c.config.StateDir, forkSnapshotID)
		}
		return err
	case protocol.OpClaudeQuestionAnswer:
		var command protocol.ClaudeAnswerCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.claudeTurns.Answer(ctx, command)
	case protocol.OpGeminiQuestionAnswer:
		var command protocol.GeminiAnswerCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.gemini.Answer(command)
	case protocol.OpGeminiTurnInterrupt:
		var command protocol.GeminiInterruptCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.interruptGeminiAndWait(ctx, command.OperationID)
	case protocol.OpGeminiTurnSteer:
		var command protocol.GeminiSteerCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.gemini.Steer(command)
	case protocol.OpCodexTurnStart:
		if c.agentUpdatePending() {
			return errors.New("Agent update is in progress; retry the task after it reconnects")
		}
		var command protocol.StartTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		command.OperationID = envelope.OperationID
		return c.codex.StartTurn(ctx, command)
	case protocol.OpCodexTurnRewrite:
		if c.agentUpdatePending() {
			return errors.New("Agent update is in progress; retry the task after it reconnects")
		}
		var command protocol.RewriteTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		command.Start.OperationID = envelope.OperationID
		return c.codex.RewriteTurn(ctx, command)
	case protocol.OpCodexTurnSteer:
		var command protocol.SteerTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.SteerTurn(ctx, command)
	case protocol.OpCodexTurnInterrupt:
		var command protocol.InterruptTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.Interrupt(ctx, command)
	case protocol.OpCodexApproval:
		var command protocol.ApprovalDecisionCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.Decide(command)
	case protocol.OpGeminiCredentialsConfigure:
		var command protocol.ConfigureGeminiCredentialsCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.configureGeminiCredentials(command)
	case protocol.OpClaudeCredentialsConfigure:
		var command protocol.ConfigureClaudeCredentialsCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.configureClaudeCredentials(command)
	case protocol.OpCredentialsConfigure, protocol.OpCodexCredentialsConfigure, protocol.OpGitCredentialsConfigure:
		var command protocol.ConfigureCredentialsCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		scopedKind, _ := protocol.CredentialOperationKind(command.Scope)
		if envelope.Kind != protocol.OpCredentialsConfigure && envelope.Kind != scopedKind {
			return errors.New("credential update scope mismatch")
		}
		return c.configureCredentials(command)
	case protocol.OpCodexUpdate:
		if !c.cliInstallMu.TryLock() {
			return errors.New("an AI CLI installation is already running")
		}
		defer c.cliInstallMu.Unlock()
		var command protocol.CodexUpdateCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		if err := c.updateCodexCLI(ctx, command); err != nil {
			return err
		}
		return c.enqueueHeartbeat(ctx)
	case protocol.OpCLIInstall:
		if !c.cliInstallMu.TryLock() {
			return errors.New("an AI CLI installation is already running")
		}
		defer c.cliInstallMu.Unlock()
		var command protocol.CLIInstallCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		if err := c.installCLI(ctx, command); err != nil {
			return err
		}
		return c.enqueueHeartbeat(ctx)
	case protocol.OpDeployStart:
		var command protocol.DeployCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.runDeployment(ctx, command)
	case protocol.OpDeployRollback:
		var command protocol.RollbackCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.runRollback(ctx, command)
	default:
		return fmt.Errorf("unsupported operation kind %q", envelope.Kind)
	}
}
