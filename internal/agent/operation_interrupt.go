package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// A restart can lose outbound messages after both receipts have been persisted.
// Replaying a successful interruption must also replay its target's receipt.
func (c *Client) replayOperationResult(ctx context.Context, envelope *protocol.ControlEnvelope, result protocol.OperationResult) {
	if envelope.Kind == "operation.interrupt" && result.Status == "succeeded" {
		var command protocol.OperationInterruptCommand
		err := json.Unmarshal(envelope.PayloadJSON, &command)
		if err == nil {
			stopContext, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = c.interruptOperation(stopContext, command)
			cancel()
		}
		if err != nil {
			result.Status, result.Message = "failed", truncate(err.Error(), 8192)
		}
	}
	c.claimInterruptedStart(envelope)
	_ = c.queue("operation_result", result, true)
}

func (c *Client) interruptOperation(ctx context.Context, command protocol.OperationInterruptCommand) error {
	if strings.TrimSpace(command.OperationID) == "" || !protocol.CanInterruptOperation(command.Kind) {
		return errors.New("operation does not support interruption")
	}
	c.seenMu.Lock()
	execution := c.seen[command.OperationID]
	if execution != nil {
		if execution.kind != command.Kind {
			c.seenMu.Unlock()
			return errors.New("operation kind mismatch")
		}
		cancel, done := execution.cancel, execution.done
		c.seenMu.Unlock()
		if cancel != nil {
			cancel()
		}
		select {
		case <-done:
			c.seenMu.Lock()
			result := execution.result
			c.seenMu.Unlock()
			return c.queue("operation_result", result, true)
		case <-ctx.Done():
			return fmt.Errorf("operation has not confirmed termination: %w", ctx.Err())
		}
	}
	result, found, err := c.loadOperationResult(command.OperationID)
	if err != nil {
		c.seenMu.Unlock()
		return errors.New("could not verify previous operation result")
	}
	if !found {
		// Serialize this tombstone with handleOperation registration. A queued or
		// delayed command must never start after its interruption is acknowledged.
		result = protocol.OperationResult{OperationID: command.OperationID, Status: "cancelled", Message: "No active execution on Agent; pending or recovered operation cancelled"}
		if err := c.saveOperationResult(result); err != nil {
			c.seenMu.Unlock()
			return errors.New("could not persist operation cancellation")
		}
		execution = &operationExecution{kind: command.Kind, done: make(chan struct{}), result: result, completedAt: time.Now(), persisted: c.config.StateDir != ""}
		close(execution.done)
		c.seen[command.OperationID] = execution
	}
	c.seenMu.Unlock()
	return c.queue("operation_result", result, true)
}

// preStartInterruptMessage marks the tombstone an interruption leaves for a
// Claude or Gemini turn whose start has not registered yet. Every envelope is
// handled on its own goroutine, so an interrupt delivered right after its
// turn can run first; the tombstone makes the late start replay this result
// instead of running a whole turn.
const preStartInterruptMessage = "Interrupted before the Agent started the task"

// preStartInterruptWait bounds how long such an interruption waits for its
// start to arrive. A start that never arrives in this process was delivered
// to an earlier Agent process, whose CLI may still be running.
var preStartInterruptWait = 5 * time.Second

// turnOwner describes the CLI turns an interrupt kind may stop.
type turnOwner struct {
	name        string
	kinds       []string
	lostMessage string
	verifyError func(error) error
}

func (owner turnOwner) owns(kind string) bool {
	for _, candidate := range owner.kinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

func interruptibleTurnStart(kind string) bool {
	return claudeTurnOwner.owns(kind) || geminiTurnOwner.owns(kind)
}

// awaitingStart reports a pre-start interruption tombstone whose turn start
// has not reached this process yet. Called under seenMu.
func (execution *operationExecution) awaitingStart() bool {
	return execution.completedAt.IsZero() && execution.result.Message == preStartInterruptMessage
}

// interruptTurnAndWait stops a Claude or Gemini turn and waits until its
// execution, including process cleanup, has finished. A turn this process
// has not seen yet is cancelled before it can start, but without its start
// the Agent cannot rule out a CLI left behind by an earlier Agent process, so
// that case still reports the ownership loss.
func (c *Client) interruptTurnAndWait(ctx context.Context, operationID string, owner turnOwner) error {
	c.seenMu.Lock()
	execution := c.seen[operationID]
	if execution != nil && owner.owns(execution.kind) {
		if execution.awaitingStart() {
			c.seenMu.Unlock()
			return c.awaitInterruptedStart(ctx, execution, owner)
		}
		cancel, done := execution.cancel, execution.done
		c.seenMu.Unlock()
		if cancel != nil {
			// Also cancels turns whose adapter has not registered the process yet.
			cancel()
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("%s process has not confirmed termination: %w", owner.name, ctx.Err())
		}
	}
	// Hold seenMu across the receipt lookup and the tombstone, like
	// interruptOperation: handleOperation registers a start under the same
	// lock, so it either is visible here or finds the tombstone.
	_, found, err := c.loadOperationResult(operationID)
	if err != nil {
		c.seenMu.Unlock()
		return owner.verifyError(err)
	}
	if found {
		c.seenMu.Unlock()
		return nil
	}
	if execution != nil {
		// Another kind of operation uses this ID; there is nothing to stop.
		c.seenMu.Unlock()
		return errors.New(owner.lostMessage)
	}
	tombstone := &operationExecution{kind: owner.kinds[0], done: make(chan struct{}), result: protocol.OperationResult{OperationID: operationID, Status: "interrupted", Message: preStartInterruptMessage}}
	close(tombstone.done)
	c.seen[operationID] = tombstone
	c.seenMu.Unlock()
	return c.awaitInterruptedStart(ctx, tombstone, owner)
}

// awaitInterruptedStart confirms the interruption once the turn's start has
// arrived and been suppressed. Otherwise the start went to an earlier Agent
// process, which is reported exactly as before: ownership is lost.
func (c *Client) awaitInterruptedStart(ctx context.Context, tombstone *operationExecution, owner turnOwner) error {
	deadline := time.NewTimer(preStartInterruptWait)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		c.seenMu.Lock()
		claimed := !tombstone.awaitingStart()
		c.seenMu.Unlock()
		if claimed {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New(owner.lostMessage)
		case <-deadline.C:
			return errors.New(owner.lostMessage)
		case <-poll.C:
		}
	}
}

// claimInterruptedStart runs when a turn start finds the tombstone of an
// earlier interruption: the turn reached this process and will never run
// here, so its interrupted result becomes the operation's durable receipt.
func (c *Client) claimInterruptedStart(envelope *protocol.ControlEnvelope) {
	if !interruptibleTurnStart(envelope.Kind) {
		return
	}
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	execution := c.seen[envelope.OperationID]
	if execution == nil || !execution.awaitingStart() {
		return
	}
	execution.kind = envelope.Kind
	if err := c.saveOperationResult(execution.result); err != nil {
		c.log.Warn("could not persist operation result", "operation_id", envelope.OperationID, "error", err)
	} else {
		execution.persisted = true
	}
	execution.completedAt = time.Now()
}
