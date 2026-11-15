package core

import (
	"time"

	"github.com/wio-platform/wio/internal/sessiontools"
)

type CollaborationSession struct {
	Engine           string     `db:"engine" json:"engine"`
	ID               string     `db:"id" json:"id"`
	Title            string     `db:"title" json:"title"`
	Status           string     `db:"status" json:"status"`
	WorkspaceID      string     `db:"workspace_id" json:"workspace_id"`
	ProjectID        string     `db:"project_id" json:"project_id"`
	ServerID         string     `db:"server_id" json:"server_id"`
	Path             string     `db:"path" json:"path"`
	Model            string     `db:"model" json:"model"`
	Mode             string     `db:"mode" json:"permission_mode"`
	Effort           string     `db:"effort" json:"effort"`
	NativeID         string     `db:"native_id" json:"-"`
	OperationID      string     `db:"operation_id" json:"operation_id"`
	AgentVersion     string     `db:"agent_version" json:"-"`
	ArchivedAt       *time.Time `db:"archived_at" json:"archived_at"`
	UpdatedAt        time.Time  `db:"updated_at" json:"updated_at"`
	URL              string     `db:"-" json:"url"`
	RecoveryPending  bool       `db:"-" json:"recovery_pending"`
	PreparationPhase string     `db:"-" json:"preparation_phase,omitempty"`
	WaitingUser      bool       `db:"-" json:"waiting_user"`
}

func (t CollaborationSession) Ref() sessiontools.Ref {
	return sessiontools.Ref{Engine: t.Engine, ID: t.ID}
}

func (t CollaborationSession) Busy() bool {
	return t.Status == "queued" || t.Status == "running" || t.Status == "interrupting" || t.Status == "preparing" || t.RecoveryPending
}

type CollaborationRequest struct {
	ID           string              `db:"id" json:"id"`
	SourceKey    string              `db:"source_key" json:"source_key"`
	RequestKey   string              `db:"request_key" json:"request_id"`
	Tool         string              `db:"tool" json:"tool"`
	Arguments    string              `db:"arguments" json:"-"`
	Execution    string              `db:"execution_json" json:"-"`
	GrantVersion int                 `db:"grant_version" json:"-"`
	Status       string              `db:"status" json:"status"`
	Phase        string              `db:"phase" json:"phase"`
	TargetEngine string              `db:"target_engine" json:"target_engine"`
	TargetID     string              `db:"target_id" json:"target_id"`
	WorkspaceID  string              `db:"workspace_id" json:"workspace_id"`
	OperationID  string              `db:"operation_id" json:"operation_id"`
	Result       string              `db:"result" json:"-"`
	CreatedAt    time.Time           `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time           `db:"updated_at" json:"updated_at"`
	Error        *sessiontools.Error `db:"-" json:"error,omitempty"`
}

type Workspace struct{ ID, ProjectID, ServerID, Path string }

type Operation struct{ Status, Result string }
