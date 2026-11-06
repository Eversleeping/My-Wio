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
