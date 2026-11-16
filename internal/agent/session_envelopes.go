package agent

import (
	"encoding/json"
	"sync/atomic"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

type controlPlaneCapabilities struct{ sessionEnvelopes atomic.Bool }

func (c *controlPlaneCapabilities) observeKeepalive(raw json.RawMessage) {
	var keepalive protocol.GeminiKeepalive
	if json.Unmarshal(raw, &keepalive) != nil {
		return
	}
	enabled := false
	for _, name := range keepalive.Capabilities {
		if name == protocol.ControlCapabilitySessionEnvelopes {
			enabled = true
			break
		}
	}
	c.sessionEnvelopes.Store(enabled)
}

type legacySessionEnvelope struct {
	engine    string
	ownership bool
	active    bool
}

var legacySessionEnvelopes = map[string]legacySessionEnvelope{
	protocol.AgentKindEvent:                 {engine: session.Codex},
	protocol.AgentKindGeminiEvent:           {engine: session.Gemini},
	protocol.AgentKindClaudeEvent:           {engine: session.Claude},
	protocol.AgentKindGeminiOwnershipLost:   {engine: session.Gemini, ownership: true},
	protocol.AgentKindGeminiOwnershipActive: {engine: session.Gemini, ownership: true, active: true},
	protocol.AgentKindClaudeOwnershipLost:   {engine: session.Claude, ownership: true},
	protocol.AgentKindClaudeOwnershipActive: {engine: session.Claude, ownership: true, active: true},
}

// Build a send-only copy. Queued and retried messages retain their old form so
// a connection to an older control plane can always select it again. A map
// preserves unknown optional fields; the typed DTOs define the known contract.
func negotiatedSessionEnvelope(envelope *protocol.AgentEnvelope) *protocol.AgentEnvelope {
	legacy, ok := legacySessionEnvelopes[envelope.Kind]
	if !ok {
		return envelope
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(envelope.PayloadJSON, &fields) != nil || fields == nil {
		return envelope
	}
	fields["engine"], _ = json.Marshal(legacy.engine)
	kind := protocol.AgentKindSessionEvent
	if legacy.ownership {
		kind = protocol.AgentKindSessionOwnership
		fields["active"], _ = json.Marshal(legacy.active)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return envelope
	}
	wire := *envelope
	wire.Kind = kind
	wire.PayloadJSON = raw
	// Legacy remains accepted by the new control plane. Keep messages that only
	// fit in that format deliverable instead of dropping them over wrapper bytes.
	if envelopeTooLarge(&wire) {
		return envelope
	}
	return &wire
}
