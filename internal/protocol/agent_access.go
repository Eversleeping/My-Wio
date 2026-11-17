package protocol

// AgentAccessSettings contains no host credentials or advanced isolation values.
type AgentAccessSettings struct {
	AllowSudo            bool                   `json:"allow_sudo"`
	SessionIsolation     bool                   `json:"session_isolation"`
	Revision             string                 `json:"revision"`
	Applied              bool                   `json:"applied"`
	RestartRequired      bool                   `json:"restart_required"`
	InstanceID           string                 `json:"instance_id,omitempty"`
	IsolationSupport     *AgentIsolationSupport `json:"isolation_support,omitempty"`
	IsolationEnvironment *IsolationEnvironment  `json:"isolation_environment,omitempty"`
}
type AgentIsolationSupport struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}
type AgentAccessCommand struct {
	ServerID  string               `json:"server_id"`
	Settings  *AgentAccessSettings `json:"settings,omitempty"`
	PlanID    string               `json:"plan_id,omitempty"`
	RequestID string               `json:"request_id,omitempty"`
}

// IsolationEnvironment is an installation plan or its actual outcome. Package
// names originate in the system helper, never in a browser request.
type IsolationEnvironment struct {
	PlanID         string             `json:"plan_id,omitempty"`
	State          string             `json:"state"`
	CanInstall     bool               `json:"can_install"`
	RequiresReboot bool               `json:"requires_reboot"`
	Packages       []IsolationPackage `json:"packages"`
	Checks         []IsolationCheck   `json:"checks"`
	Logs           []string           `json:"logs,omitempty"`
	Error          string             `json:"error,omitempty"`
}
type IsolationPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type IsolationCheck struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}
