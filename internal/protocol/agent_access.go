package protocol

// AgentAccessSettings contains no host credentials or advanced isolation values.
type AgentAccessSettings struct {
	AllowSudo        bool                   `json:"allow_sudo"`
	SessionIsolation bool                   `json:"session_isolation"`
	Revision         string                 `json:"revision"`
	Applied          bool                   `json:"applied"`
	RestartRequired  bool                   `json:"restart_required"`
	InstanceID       string                 `json:"instance_id,omitempty"`
	IsolationSupport *AgentIsolationSupport `json:"isolation_support,omitempty"`
}
type AgentIsolationSupport struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}
type AgentAccessCommand struct {
	ServerID string               `json:"server_id"`
	Settings *AgentAccessSettings `json:"settings,omitempty"`
}
