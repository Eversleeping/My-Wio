package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/wio-platform/wio/internal/protocol"
)

// maxAgentMessageBytes matches the gRPC send/receive limit on both ends of the
// Agent stream. gRPC kills the whole stream on an oversized message, so every
// envelope is checked against it before it can enter the outbound queue.
const maxAgentMessageBytes = 8 << 20

// Upper bound for the JSON field names, quoting and the fixed-size fields of an
// AgentEnvelope around its already-encoded payload.
const agentEnvelopeOverhead = 512

var errOutboundQueueFull = errors.New("agent outbound queue is full")

func encodedEnvelopeSize(envelope *protocol.AgentEnvelope) int {
	return len(envelope.PayloadJSON) + len(envelope.MessageID) + len(envelope.ServerID) + len(envelope.Kind) + agentEnvelopeOverhead
}

func envelopeTooLarge(envelope *protocol.AgentEnvelope) bool {
	return encodedEnvelopeSize(envelope) > maxAgentMessageBytes
}

// operationResultTooLargeMessage keeps the failure actionable: the work may
// have completed, but its result cannot cross the stream in one message.
func operationResultTooLargeMessage(size int) string {
	return fmt.Sprintf("result too large: %d bytes exceeds the %d byte Agent message limit", size, maxAgentMessageBytes-agentEnvelopeOverhead)
}

// limitOperationResult replaces a result whose payload cannot be delivered by a
// small failure. It runs before the receipt is persisted so replays after a
// reconnect or restart deliver the same small failure instead of retrying the
// oversized message forever.
func limitOperationResult(result protocol.OperationResult) protocol.OperationResult {
	// Data is embedded verbatim; the bounded Message can at most expand six-fold
	// when every byte needs a \u escape. Only marshal when the cheap bound fails.
	estimate := len(result.Data) + 6*len(result.Message) + len(result.OperationID) + len(result.Status) + 2*agentEnvelopeOverhead
	if estimate <= maxAgentMessageBytes {
		return result
	}
	if raw, err := json.Marshal(result); err == nil && len(raw)+agentEnvelopeOverhead <= maxAgentMessageBytes {
		return result
	}
	return protocol.OperationResult{OperationID: result.OperationID, Status: "failed", Message: operationResultTooLargeMessage(len(result.Data))}
}

func (c *Client) newEnvelope(kind string, raw json.RawMessage) *protocol.AgentEnvelope {
	return &protocol.AgentEnvelope{MessageID: uuid.NewString(), ServerID: c.config.ServerID, Kind: kind, OccurredAtUnixMS: time.Now().UnixMilli(), PayloadJSON: raw}
}

// boundedEnvelope returns an envelope that fits the stream limit, or nil when
// the message must be dropped. Operation results become failed results so the
// control plane still releases the operation; other messages (events,
// inventory) are dropped with a warning because retrying cannot succeed.
func (c *Client) boundedEnvelope(envelope *protocol.AgentEnvelope) *protocol.AgentEnvelope {
	if !envelopeTooLarge(envelope) {
		return envelope
	}
	return c.replaceOversized(envelope)
}

func (c *Client) replaceOversized(envelope *protocol.AgentEnvelope) *protocol.AgentEnvelope {
	size := len(envelope.PayloadJSON)
	if envelope.Kind == "operation_result" {
		var result protocol.OperationResult
		if err := json.Unmarshal(envelope.PayloadJSON, &result); err == nil && result.OperationID != "" {
			replacement := protocol.OperationResult{OperationID: result.OperationID, Status: "failed", Message: operationResultTooLargeMessage(size)}
			raw, err := json.Marshal(replacement)
			if err == nil {
				c.warn("operation result exceeds the Agent message limit; reporting failure instead", "operation_id", result.OperationID, "bytes", size)
				bounded := *envelope
				bounded.PayloadJSON = raw
				return &bounded
			}
		}
	}
	c.warn("dropping Agent message that exceeds the message limit", "kind", envelope.Kind, "bytes", size)
	return nil
}

func (c *Client) warn(message string, args ...any) {
	if c.log != nil {
		c.log.Warn(message, args...)
	}
}

func (c *Client) enqueue(envelope *protocol.AgentEnvelope, important bool) error {
	if envelope = c.boundedEnvelope(envelope); envelope == nil {
		// Dropped deliberately; reporting an error would make producers such as
		// AI event streams abort a healthy turn over one oversized notification.
		return nil
	}
	if important {
		select {
		case c.outbound <- envelope:
			return nil
		case <-time.After(5 * time.Second):
			return errOutboundQueueFull
		}
	}
	select {
	case c.outbound <- envelope:
	default:
	}
	return nil
}

// queueTracked queues an important message and returns a channel closed once
// sendLoop has handed it to the stream. The self-update path waits on it so the
// update result (and everything queued before it) leaves before exec.
func (c *Client) queueTracked(kind string, payload any) (<-chan struct{}, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	envelope := c.boundedEnvelope(c.newEnvelope(kind, raw))
	if envelope == nil {
		return nil, errors.New("message exceeds the Agent message limit")
	}
	sent := make(chan struct{})
	c.sentMu.Lock()
	if c.sentWaiters == nil {
		c.sentWaiters = make(map[string]chan struct{})
	}
	c.sentWaiters[envelope.MessageID] = sent
	c.sentMu.Unlock()
	if err := c.enqueue(envelope, true); err != nil {
		c.sentMu.Lock()
		delete(c.sentWaiters, envelope.MessageID)
		c.sentMu.Unlock()
		return nil, err
	}
	return sent, nil
}

func (c *Client) markSent(envelope *protocol.AgentEnvelope) {
	c.sentMu.Lock()
	if sent := c.sentWaiters[envelope.MessageID]; sent != nil {
		close(sent)
		delete(c.sentWaiters, envelope.MessageID)
	}
	c.sentMu.Unlock()
}

// A message whose Send failed is retried first on the next connection. Putting
// it back at the tail of the outbound queue would reorder events (for example a
// turn start after its completion) or silently drop it when the queue is full.
func (c *Client) takeRetry() *protocol.AgentEnvelope {
	c.sentMu.Lock()
	defer c.sentMu.Unlock()
	envelope := c.retry
	c.retry = nil
	return envelope
}

func (c *Client) setRetry(envelope *protocol.AgentEnvelope) {
	c.sentMu.Lock()
	c.retry = envelope
	c.sentMu.Unlock()
}
