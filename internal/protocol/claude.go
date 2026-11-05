package protocol

const ClaudeOwnershipLostMessage = "Agent no longer owns this Claude task; verify the old CLI process before recovering the session"

// ClaudeTurnCommand contains no credentials. The CLI authenticates as the Agent account.
type ClaudeTurnCommand struct {
	ThreadID       string `json:"thread_id"`
	Workspace      string `json:"workspace"`
	SessionID      string `json:"session_id"`
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
	PermissionMode string `json:"permission_mode"`
	Effort         string `json:"effort,omitempty"`
}

func ValidClaudeEffort(effort string) bool {
	return effort == "" || effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh" || effort == "max"
}

type ClaudeInterruptCommand struct {
	ThreadID        string `json:"thread_id"`
	OperationID     string `json:"operation_id"`
	RecoveryPending bool   `json:"recovery_pending,omitempty"`
}
