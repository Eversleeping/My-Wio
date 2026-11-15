package session

import "time"

type ScheduledTask struct {
	ThinkingLevel   string     `db:"thinking_level" json:"thinking_level"`
	AgentKind       string     `db:"agent_kind" json:"agent_kind"`
	ID              string     `db:"id" json:"id"`
	ThreadID        string     `db:"thread_id" json:"thread_id"`
	ThreadTitle     string     `db:"thread_title" json:"thread_title"`
	WorkspaceID     string     `db:"workspace_id" json:"workspace_id"`
	ProjectID       string     `db:"project_id" json:"project_id"`
	ProjectName     string     `db:"project_name" json:"project_name"`
	ServerID        string     `db:"server_id" json:"server_id"`
	ServerName      string     `db:"server_name" json:"server_name"`
	ServerStatus    string     `db:"server_status" json:"server_status"`
	CodexThreadID   string     `db:"codex_thread_id" json:"codex_thread_id"`
	WorkspacePath   string     `db:"workspace_path" json:"workspace_path"`
	Name            string     `db:"name" json:"name"`
	Prompt          string     `db:"prompt" json:"prompt"`
	Schedule        string     `db:"schedule" json:"schedule"`
	Timezone        string     `db:"timezone" json:"timezone"`
	Enabled         bool       `db:"enabled" json:"enabled"`
	Model           string     `db:"model" json:"model"`
	ReasoningEffort string     `db:"reasoning_effort" json:"reasoning_effort"`
	ApprovalMode    string     `db:"approval_mode" json:"approval_mode"`
	NextRunAt       time.Time  `db:"next_run_at" json:"next_run_at"`
	LastRunAt       *time.Time `db:"last_run_at" json:"last_run_at"`
	LastRunStatus   string     `db:"last_run_status" json:"last_run_status"`
	LastRunMessage  string     `db:"last_run_message" json:"last_run_message"`
	LastOperationID string     `db:"last_operation_id" json:"last_operation_id"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
}

type ScheduledTaskInput struct {
	ThinkingLevel   string
	AgentKind       string
	ThreadID        string
	Name            string
	Prompt          string
	Schedule        string
	Timezone        string
	Enabled         bool
	Model           string
	ReasoningEffort string
	ApprovalMode    string
}
