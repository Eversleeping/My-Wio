package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wio-platform/wio/internal/protocol"
)

var geminiTurnOwner = turnOwner{
	name:        "Gemini",
	kinds:       []string{"gemini.turn.start", "gemini.turn.rewrite"},
	lostMessage: protocol.GeminiOwnershipLostMessage,
	verifyError: func(err error) error { return fmt.Errorf("could not verify Gemini task completion: %w", err) },
}

// A control-message acknowledgement is not proof that the CLI stopped. Wait
// for the original execution (including process cleanup) before confirming.
func (c *Client) interruptGeminiAndWait(ctx context.Context, operationID string) error {
	return c.interruptTurnAndWait(ctx, operationID, geminiTurnOwner)
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
		if execution != nil && !execution.awaitingStart() {
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
		if execution != nil {
			// Only an interruption that beat this turn's start is known; it says
			// nothing about a CLI that an earlier Agent process started.
			c.seenMu.Unlock()
			_ = c.queue("gemini_ownership_lost", protocol.GeminiOwnership{OperationID: id}, true)
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
