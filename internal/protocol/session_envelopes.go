package protocol

// ControlCapabilitySessionEnvelopes is advertised by the control plane, not by
// Agents. It permits session_event/session_ownership on this connection.
const ControlCapabilitySessionEnvelopes = "session.envelopes"

// SessionEvent adds an engine discriminator to the existing event fields.
// Native event kinds and contents keep their original meaning.
type SessionEvent struct {
	Engine string `json:"engine"`
	StreamEvent
}

// SessionOwnership replaces the four engine-specific ownership envelopes.
// Codex continues to report its native turn lifecycle through session events.
type SessionOwnership struct {
	Engine      string `json:"engine"`
	OperationID string `json:"operation_id"`
	Active      bool   `json:"active"`
}
