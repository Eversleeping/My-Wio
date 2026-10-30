package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wio-platform/wio/internal/protocol"
)

// A control-message acknowledgement is not proof that the CLI stopped. Wait
// for the original execution (including process cleanup) before confirming.
func (c *Client) interruptGeminiAndWait(ctx context.Context, operationID string) error {
	c.seenMu.Lock()
	execution := c.seen[operationID]
	if execution != nil && (execution.kind == "gemini.turn.start" || execution.kind == "gemini.turn.rewrite") {
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
			return fmt.Errorf("Gemini process has not confirmed termination: %w", ctx.Err())
		}
	}
	c.seenMu.Unlock()
	if _, found, err := c.loadOperationResult(operationID); err != nil {
		return fmt.Errorf("could not verify Gemini task completion: %w", err)
	} else if found {
		return nil
	}
	return errors.New(protocol.GeminiOwnershipLostMessage)
}

// A probe never starts or interrupts a turn. Replaying a completed receipt
// repairs a lost result; an unknown owner is reported without claiming that an
// orphaned CLI has stopped.
func (c *Client) reconcileGeminiOperations(raw json.RawMessage) {
	var check protocol.GeminiKeepalive
	if len(raw) == 0 || json.Unmarshal(raw, &check) != nil {
		return
	}
	for _, id := range check.OperationIDs {
		c.seenMu.Lock()
		execution := c.seen[id]
		if execution != nil {
			select {
			case <-execution.done:
				result := execution.result
				c.seenMu.Unlock()
				_ = c.queue("operation_result", result, true)
			default:
				c.seenMu.Unlock()
				_ = c.queue("gemini_ownership_active", protocol.GeminiOwnership{OperationID: id}, true)
			}
			continue
		}
		result, found, err := c.loadOperationResult(id)
		c.seenMu.Unlock()
		if err != nil {
			c.log.Warn("could not inspect Gemini operation receipt", "operation_id", id, "error", err)
			continue
		}
		if found {
			_ = c.queue("operation_result", result, true)
		} else {
			_ = c.queue("gemini_ownership_lost", protocol.GeminiOwnership{OperationID: id}, true)
		}
	}
}
