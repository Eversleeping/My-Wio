package session

import "time"

type Record struct {
	Engine          string       `db:"engine" json:"engine"`
	ID              string       `db:"id" json:"id"`
	WorkspaceID     string       `db:"workspace_id" json:"workspace_id"`
	ProjectID       string       `db:"project_id" json:"project_id"`
	ServerID        string       `db:"server_id" json:"server_id"`
	Path            string       `db:"path" json:"path"`
	WorkspaceName   string       `db:"workspace_name" json:"workspace_name"`
	ProjectName     string       `db:"project_name" json:"project_name"`
	ServerName      string       `db:"server_name" json:"server_name"`
	ServerStatus    string       `db:"-" json:"server_status"`
	NativeID        string       `db:"native_id" json:"native_id"`
	Title           string       `db:"title" json:"title"`
	Status          string       `db:"status" json:"status"`
	Model           string       `db:"model" json:"model"`
	Mode            string       `db:"mode" json:"mode"`
	Effort          string       `db:"effort" json:"effort"`
	OperationID     string       `db:"operation_id" json:"operation_id"`
	PinnedAt        *time.Time   `db:"pinned_at" json:"pinned_at"`
	ArchivedAt      *time.Time   `db:"archived_at" json:"archived_at"`
	ProjectPinnedAt *time.Time   `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt *time.Time   `db:"project_hidden_at" json:"project_hidden_at"`
	CreatedAt       time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time    `db:"updated_at" json:"updated_at"`
	AgentVersion    string       `db:"agent_version" json:"-"`
	CLIVersion      string       `db:"cli_version" json:"-"`
	LastSeenAt      *time.Time   `db:"last_seen_at" json:"-"`
	ServerRevokedAt *time.Time   `db:"server_revoked_at" json:"-"`
	ExecutionJSON   string       `db:"execution_json" json:"-"`
	Capabilities    Capabilities `db:"-" json:"capabilities"`
}

func (t Record) Ref() Ref { return Ref{Engine: t.Engine, ID: t.ID} }
