package agent

import (
	"encoding/json"

	"github.com/wio-platform/wio/internal/protocol"
)

// Results are fire-and-forget: a full outbound queue, a stream that dies
// mid-flight or an Agent restart can lose them after the control plane saw the
// operation start. The control plane lists overdue operations in its keepalive;
// answer each from the execution table or the on-disk receipt, and only claim
// "unknown" when neither exists, which is the one answer that fails the
// operation. Like the Gemini probe, this never starts or repeats an operation.
func (c *Client) reconcileOperations(raw json.RawMessage) {
	var check protocol.GeminiKeepalive
	if len(raw) == 0 || json.Unmarshal(raw, &check) != nil {
		return
	}
	for _, id := range check.ReconcileOperationIDs {
		if id == "" {
			continue
		}
		// Hold seenMu across the receipt lookup so a concurrently registering
		// execution of the same ID is observed either here or in c.seen.
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
				_ = c.queue(protocol.AgentKindOperationReconcile, protocol.OperationReconcile{OperationID: id, State: protocol.OperationReconcileActive}, true)
			}
			continue
		}
		result, found, err := c.loadOperationResult(id)
		c.seenMu.Unlock()
		if err != nil {
			// An unreadable receipt does not prove the operation never ran.
			c.log.Warn("could not inspect operation receipt", "operation_id", id, "error", err)
			continue
		}
		if found {
			_ = c.queue("operation_result", result, true)
			continue
		}
		_ = c.queue(protocol.AgentKindOperationReconcile, protocol.OperationReconcile{OperationID: id, State: protocol.OperationReconcileUnknown}, true)
	}
}
