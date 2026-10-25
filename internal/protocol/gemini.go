package protocol

// GeminiTurnCommand runs a headless Gemini CLI turn in an existing workspace.
// Authentication stays on the Agent host; credentials never enter this payload.
type GeminiTurnCommand struct {
	ThreadID     string               `json:"thread_id"`
	Workspace    string               `json:"workspace"`
	SessionID    string               `json:"session_id"`
	Prompt       string               `json:"prompt"`
	Model        string               `json:"model"`
	ApprovalMode string               `json:"approval_mode"`
	Rewrite      *GeminiRewriteTarget `json:"rewrite,omitempty"`
}

type GeminiInterruptCommand struct {
	ThreadID    string `json:"thread_id"`
	OperationID string `json:"operation_id"`
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
