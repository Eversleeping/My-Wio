package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/wio-platform/wio/internal/protocol"
)

var claudeTurnOwner = turnOwner{
	name:        "Claude",
	kinds:       []string{"claude.turn.start"},
	lostMessage: protocol.ClaudeOwnershipLostMessage,
	verifyError: func(error) error { return errors.New("could not verify Claude task completion") },
}

func (c *Client) interruptClaudeAndWait(ctx context.Context, operationID string) error {
	return c.interruptTurnAndWait(ctx, operationID, claudeTurnOwner)
}

func (c *Client) reconcileClaudeOperations(raw json.RawMessage) {
	var check protocol.GeminiKeepalive
	if len(raw) == 0 || json.Unmarshal(raw, &check) != nil {
		return
	}
	for _, id := range check.ClaudeOperationIDs {
		c.seenMu.Lock()
		execution := c.seen[id]
		if execution != nil && !execution.awaitingStart() {
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
		if execution != nil {
			// Only an interruption that beat this turn's start is known; it says
			// nothing about a CLI that an earlier Agent process started.
			c.seenMu.Unlock()
			_ = c.queue("claude_ownership_lost", protocol.GeminiOwnership{OperationID: id}, true)
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
