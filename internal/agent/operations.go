package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wio-platform/wio/internal/protocol"
	"strings"
	"time"
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
	} else {
		restartPath, resultData, refreshInventory, err = c.dispatchOperation(ctx, envelope)
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
		return c.executeInventoryScan(ctx, envelope)
	case protocol.OpGitImport:
		return c.executeGitImport(ctx, envelope)
	case protocol.OpGeminiTurnStart, protocol.OpGeminiTurnRewrite:
		return c.executeGeminiTurnStart(ctx, envelope)
	case protocol.OpClaudeTurnStart:
		return c.executeClaudeTurnStart(ctx, envelope)
	case protocol.OpClaudeQuestionAnswer:
		return c.executeClaudeQuestionAnswer(ctx, envelope)
	case protocol.OpGeminiQuestionAnswer:
		return c.executeGeminiQuestionAnswer(ctx, envelope)
	case protocol.OpGeminiTurnInterrupt:
		return c.executeGeminiTurnInterrupt(ctx, envelope)
	case protocol.OpGeminiTurnSteer:
		return c.executeGeminiTurnSteer(ctx, envelope)
	case protocol.OpCodexTurnStart:
		return c.executeCodexTurnStart(ctx, envelope)
	case protocol.OpCodexTurnRewrite:
		return c.executeCodexTurnRewrite(ctx, envelope)
	case protocol.OpCodexTurnSteer:
		return c.executeCodexTurnSteer(ctx, envelope)
	case protocol.OpCodexTurnInterrupt:
		return c.executeCodexTurnInterrupt(ctx, envelope)
	case protocol.OpCodexApproval:
		return c.executeCodexApproval(ctx, envelope)
	case protocol.OpGeminiCredentialsConfigure:
		return c.executeGeminiCredentialsConfigure(ctx, envelope)
	case protocol.OpClaudeCredentialsConfigure:
		return c.executeClaudeCredentialsConfigure(ctx, envelope)
	case protocol.OpCredentialsConfigure, protocol.OpCodexCredentialsConfigure, protocol.OpGitCredentialsConfigure:
		return c.executeCredentialsConfigure(ctx, envelope)
	case protocol.OpCodexUpdate:
		return c.executeCodexUpdate(ctx, envelope)
	case protocol.OpCLIInstall:
		return c.executeCLIInstall(ctx, envelope)
	case protocol.OpDeployStart:
		return c.executeDeployStart(ctx, envelope)
	case protocol.OpDeployRollback:
		return c.executeDeployRollback(ctx, envelope)
	default:
		return fmt.Errorf("unsupported operation kind %q", envelope.Kind)
	}
}

func (c *Client) dispatchOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
	if envelope.Kind == protocol.OpOperationInterrupt {
		return c.runOperationInterruptOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpAgentUpdate {
		return c.runAgentUpdateOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGeminiTurnInterrupt {
		return c.runGeminiTurnInterruptOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpClaudeTurnSteer {
		return c.runClaudeTurnSteerOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpClaudeTurnInterrupt {
		return c.runClaudeTurnInterruptOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGeminiSkillsManage {
		return c.runGeminiSkillsManageOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpClaudeSkillsList {
		return c.runClaudeSkillsListOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGeminiSkillsList {
		return c.runGeminiSkillsListOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpWorkspaceFiles {
		return c.runWorkspaceFilesOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpWorkspaceFileWrite {
		return c.runWorkspaceFileWriteOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpWorkspaceFilePreview || envelope.Kind == protocol.OpWorkspaceImagePreview {
		return c.runWorkspaceFilePreviewOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpWorkspaceChanges {
		return c.runWorkspaceChangesOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpWorkspaceDiffPreview {
		return c.runWorkspaceDiffPreviewOperation(ctx, envelope)
	} else if strings.HasPrefix(envelope.Kind, protocol.OpCodexGoalPrefix) || envelope.Kind == protocol.OpCodexMCPList || envelope.Kind == protocol.OpCodexSkillsList || envelope.Kind == protocol.OpCodexModelsList || envelope.Kind == protocol.OpCodexStatusSnapshot || envelope.Kind == protocol.OpCodexThreadCompact {
		return c.runCodexGoalPrefixOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpCodexThreadFork {
		return c.runCodexThreadForkOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitProjectCreate {
		return c.runGitProjectCreateOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitProjectDelete {
		return c.runGitProjectDeleteOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitWorkspaceInspect {
		return c.runGitWorkspaceInspectOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitWorkspaceWrite {
		return c.runGitWorkspaceWriteOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitWorkspaceLifecycle {
		return c.runGitWorkspaceLifecycleOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitWorkspaceClone {
		return c.runGitWorkspaceCloneOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpClaudeThreadFork {
		return c.runClaudeThreadForkOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGeminiThreadFork {
		return c.runGeminiThreadForkOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitWorktreeCreate {
		return c.runGitWorktreeCreateOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpGitWorktreeCleanup {
		return c.runGitWorktreeCleanupOperation(ctx, envelope)
	} else if envelope.Kind == protocol.OpDeployContainer || strings.HasPrefix(envelope.Kind, protocol.OpDeployContainerPrefix) {
		return c.runDeployContainerOperation(ctx, envelope)
	} else {
		return "", nil, false, c.execute(ctx, envelope)
	}
}

func (c *Client) executeInventoryScan(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	return c.enqueueInventory(ctx)
}
