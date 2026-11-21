package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
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
	parent = c.processContext(parent)
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
		restartErr := error(nil)
		if strings.HasPrefix(restartPath, runtimeAccessRestart+":") || strings.HasPrefix(restartPath, runtimeServiceRestart+":") || restartPath == "platform-service-restart" {
			if !resultSaved {
				restartErr = errors.New("runtime settings receipt was not persisted; refusing to restart")
			} else {
				restartErr = c.restartRuntimeAccess(parent, restartPath)
			}
		} else {
			restartErr = activateStagedUpdate(c.config.StateDir, restartPath)
		}
		if restartErr != nil {
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

// execute preserves the historical error-only entry point used by local callers.
// In particular Gemini interruption keeps its original parent-context lifetime.
func (c *Client) execute(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	ctx = childprocess.WithExtraEnvironment(ctx, c.config.ExtraEnvironment)
	if handler, ok := operationHandlers[envelope.Kind]; ok && handler.execute != nil {
		return handler.execute(c, ctx, envelope)
	}
	return fmt.Errorf("unsupported operation kind %q", envelope.Kind)
}

func (c *Client) dispatchOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
	if runtime.GOOS != "linux" && strings.HasPrefix(envelope.Kind, protocol.OpDeployPrefix) {
		return "", nil, false, errors.New("deployment operations require a Linux Agent")
	}
	ctx = childprocess.WithExtraEnvironment(ctx, c.config.ExtraEnvironment)
	if err := c.verifyTaskReview(ctx, envelope); err != nil {
		return "", nil, false, err
	}
	if handler, ok := operationHandlerFor(envelope.Kind); ok {
		return handler(c, ctx, envelope)
	}
	return "", nil, false, fmt.Errorf("unsupported operation kind %q", envelope.Kind)
}

func (c *Client) executeInventoryScan(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	return c.enqueueInventory(ctx)
}
