package protocol

const (
	IsolationDisabled    = "disabled"
	IsolationEnabled     = "enabled"
	IsolationUnsupported = "unsupported"
)

// SessionIsolationStatus is additive. An absent status identifies an Agent
// which has not reported this feature; it must never be treated as enabled.
type SessionIsolationStatus struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}
