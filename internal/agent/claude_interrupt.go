package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wio-platform/wio/internal/protocol"
)

func (c *Client) interruptClaudeAndWait(ctx context.Context, operationID string) error {
	c.seenMu.Lock()
	execution := c.seen[operationID]
	if execution != nil && execution.kind == "claude.turn.start" {
		cancel, done := execution.cancel, execution.done
		c.seenMu.Unlock()
		if cancel != nil {
			cancel()
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("Claude process has not confirmed termination: %w", ctx.Err())
		}
	}
	c.seenMu.Unlock()
	if _, found, err := c.loadOperationResult(operationID); err != nil {
		return errors.New("could not verify Claude task completion")
	} else if found {
		return nil
	}
	return errors.New(protocol.ClaudeOwnershipLostMessage)
}

func (c *Client) reconcileClaudeOperations(raw json.RawMessage) {
	var check protocol.GeminiKeepalive
	if len(raw) == 0 || json.Unmarshal(raw, &check) != nil {
		return
	}
	for _, id := range check.ClaudeOperationIDs {
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
				_ = c.queue("claude_ownership_active", protocol.GeminiOwnership{OperationID: id}, true)
			}
			continue
		}
		result, found, err := c.loadOperationResult(id)
		c.seenMu.Unlock()
		if err != nil {
			c.log.Warn("could not inspect Claude operation receipt", "operation_id", id, "error", err)
			continue
		}
		if found {
			_ = c.queue("operation_result", result, true)
		} else {
			_ = c.queue("claude_ownership_lost", protocol.GeminiOwnership{OperationID: id}, true)
		}
	}
}
