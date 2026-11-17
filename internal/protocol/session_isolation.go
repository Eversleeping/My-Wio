package protocol

const (
	IsolationDisabled    = "disabled"
	IsolationEnabled     = "enabled"
	IsolationUnsupported = "unsupported"
)

// SessionIsolationStatus decodes historical heartbeats and wire fixtures only.
// Current Agents omit it and the control plane ignores it.
type SessionIsolationStatus struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}
