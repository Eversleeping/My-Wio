package protocol

const GeminiOwnershipLostMessage = "Agent no longer owns this Gemini task; it may have restarted. Verify the old CLI process before recovering the session"

// GeminiKeepalive requests a status check without replaying a turn.
type GeminiKeepalive struct {
	OperationIDs []string `json:"gemini_operation_ids,omitempty"`
}

type GeminiOwnership struct {
	OperationID string `json:"operation_id"`
}

// GeminiTurnCommand runs a headless Gemini CLI turn in an existing workspace.
// Authentication stays on the Agent host; credentials never enter this payload.
type GeminiTurnCommand struct {
	Steering        bool                 `json:"steering,omitempty"`
	SteeringContext []string             `json:"steering_context,omitempty"`
	Interactive     bool                 `json:"interactive,omitempty"`
	Images          []TurnImage          `json:"images,omitempty"`
	ThinkingLevel   string               `json:"thinking_level,omitempty"`
	ThreadID        string               `json:"thread_id"`
	Workspace       string               `json:"workspace"`
	SessionID       string               `json:"session_id"`
	Prompt          string               `json:"prompt"`
	Model           string               `json:"model"`
	ApprovalMode    string               `json:"approval_mode"`
	Rewrite         *GeminiRewriteTarget `json:"rewrite,omitempty"`
}

type GeminiSteerCommand struct {
	ThreadID    string `json:"thread_id"`
	OperationID string `json:"operation_id"`
	RequestID   string `json:"request_id"`
	Prompt      string `json:"prompt"`
}

type GeminiInterruptResult struct {
	OperationID string `json:"operation_id"`
	Stopped     bool   `json:"stopped"`
}

type GeminiAnswerCommand struct {
	ThreadID    string                     `json:"thread_id"`
	OperationID string                     `json:"operation_id"`
	RequestID   string                     `json:"request_id"`
	Answers     map[string]UserInputAnswer `json:"answers"`
}

type GeminiInterruptCommand struct {
	ThreadID        string `json:"thread_id"`
	OperationID     string `json:"operation_id"`
	RecoveryPending bool   `json:"recovery_pending,omitempty"`
}

// GeminiRewriteTarget identifies the original native turn by the CLI's own
// timestamp, independent of clock differences between Agent and control plane.
type GeminiRewriteTarget struct {
	EventID   string `json:"event_id"`
	StartedAt string `json:"started_at"`
	Before    string `json:"before,omitempty"`
}

// GeminiForkCommand snapshots native history without invoking the model.
type GeminiForkCommand struct {
	SourceThreadID  string               `json:"source_thread_id"`
	TargetThreadID  string               `json:"target_thread_id"`
	SessionID       string               `json:"session_id"`
	SourceWorkspace string               `json:"source_workspace"`
	WorkspaceID     string               `json:"workspace_id"`
	Workspace       string               `json:"workspace"`
	Title           string               `json:"title"`
	CutoffSequence  int64                `json:"cutoff_sequence"`
	Before          *GeminiRewriteTarget `json:"before,omitempty"`
}
type GeminiForkResult struct {
	SessionID string `json:"session_id"`
}
