package agent

import (
	"context"
	"errors"
	"fmt"
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
	return errors.New("Agent no longer owns this Gemini task; it may have restarted. Verify the old CLI process before recovering the session")
}
