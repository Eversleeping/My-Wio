// Package task defines task planning contracts without persistence or transport.
package task

import (
	"encoding/json"
	"time"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string        { return e.Message }
func Fail(code, message string) error { return &Error{code, message} }

const (
	Invalid             = "INVALID_ARGUMENT"
	Conflict            = "VERSION_CONFLICT"
	IdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	StateConflict       = "STATE_CONFLICT"
	Unsupported         = "CAPABILITY_UNSUPPORTED"
	Forbidden           = "AUTHORIZATION_REQUIRED"
)

type Meta struct {
	Actor           string `json:"-"`
	RequestID       string `json:"request_id"`
	ExpectedVersion int64  `json:"expected_version,omitempty"`
}
type Criterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type Counters struct {
	Plans    int `json:"plans"`
	Runs     int `json:"runs"`
	Attempts int `json:"attempts"`
	Messages int `json:"messages"`
}
type Task struct {
	ID                   string      `json:"id"`
	ProjectID            string      `json:"project_id"`
	Title                string      `json:"title"`
	Requirement          string      `json:"requirement"`
	Criteria             []Criterion `json:"criteria"`
	SpecVersion          int64       `json:"spec_version"`
	CurrentPlanID        *string     `json:"current_plan_id"`
	CurrentRunID         *string     `json:"current_run_id"`
	CurrentGrantRevision *int64      `json:"current_grant_revision"`
	Counters             Counters    `json:"counters"`
	State                string      `json:"state"`
	Version              int64       `json:"version"`
	CreatedBy            string      `json:"created_by"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
	ArchivedAt           *time.Time  `json:"archived_at"`
}
type Create struct {
	Meta
	ProjectID   string      `json:"project_id"`
	Title       string      `json:"title"`
	Requirement string      `json:"requirement"`
	Criteria    []Criterion `json:"criteria"`
}
type Update struct {
	Meta
	Title       *string      `json:"title,omitempty"`
	Requirement *string      `json:"requirement,omitempty"`
	Criteria    *[]Criterion `json:"criteria,omitempty"`
}
type Member struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	Engine        string `json:"engine"`
	WorkspaceID   string `json:"workspace_id"`
	Model         string `json:"model"`
	Mode          string `json:"mode"`
	Effort        string `json:"effort"`
	DirectoryMode string `json:"directory_mode"`
}
type Input struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	StepID string `json:"step_id,omitempty"`
}
type Step struct {
	ID               string   `json:"id"`
	MemberID         string   `json:"member_id"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	Instructions     string   `json:"instructions"`
	Inputs           []Input  `json:"input_refs"`
	DependsOn        []string `json:"depends_on"`
	CriterionIDs     []string `json:"criterion_ids"`
	Required         bool     `json:"required"`
	CompletionPolicy string   `json:"completion_policy"`
}
type AppendPlan struct {
	Meta
	ParentID   *string  `json:"parent_id,omitempty"`
	ProposalID string   `json:"proposal_id,omitempty"`
	Members    []Member `json:"members"`
	Steps      []Step   `json:"steps"`
	Reason     string   `json:"reason"`
}
type Plan struct {
	ID            string      `json:"id"`
	TaskID        string      `json:"task_id"`
	Revision      int64       `json:"revision"`
	ParentID      *string     `json:"parent_id"`
	SpecVersion   int64       `json:"spec_version"`
	Requirement   string      `json:"requirement_snapshot"`
	Criteria      []Criterion `json:"criteria_snapshot"`
	Members       []Member    `json:"members"`
	Steps         []Step      `json:"steps"`
	GrantRevision *int64      `json:"grant_revision"`
	Reason        string      `json:"reason"`
	Author        string      `json:"author"`
	ContentHash   string      `json:"content_hash"`
	CreatedAt     time.Time   `json:"created_at"`
}
type PlanResult struct {
	Plan        Plan  `json:"plan"`
	TaskVersion int64 `json:"task_version"`
}
type Acceptance struct {
	Meta
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
	EvidenceHash string `json:"evidence_hash"`
}
type Archive struct {
	Meta
	Archived *bool `json:"archived"`
}
type Run struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	PlanID           string     `json:"plan_id"`
	State            string     `json:"state"`
	Version          int64      `json:"version"`
	GrantRevision    int64      `json:"grant_revision"`
	StartedAt        *time.Time `json:"started_at"`
	DeadlineAt       *time.Time `json:"deadline_at"`
	EndedAt          *time.Time `json:"ended_at"`
	AcceptedBy       *string    `json:"accepted_by,omitempty"`
	AcceptanceReason *string    `json:"acceptance_reason,omitempty"`
	AcceptedAt       *time.Time `json:"accepted_at,omitempty"`
}
type StepRun struct {
	StepID           string  `json:"step_id"`
	State            string  `json:"state"`
	Version          int64   `json:"version"`
	CurrentAttemptID *string `json:"current_attempt_id"`
	BlockCode        *string `json:"block_code"`
	ArtifactID       *string `json:"artifact_id,omitempty"`
	Reworks          int     `json:"reworks,omitempty"`
}
type Detail struct {
	Task                   Task            `json:"task"`
	Plan                   *Plan           `json:"plan"`
	Run                    *Run            `json:"run"`
	Steps                  []StepRun       `json:"steps"`
	Members                []Member        `json:"members"`
	Blockers               []string        `json:"blockers"`
	Capabilities           map[string]bool `json:"capabilities"`
	RunPlan                *Plan           `json:"run_plan,omitempty"`
	AcceptanceEvidenceHash string          `json:"acceptance_evidence_hash,omitempty"`
	LastSequence           int64           `json:"last_sequence"`
	Grant                  *Grant          `json:"grant"`
	Attempts               []Attempt       `json:"attempts"`
}
type Event struct {
	EventID       string          `json:"event_id"`
	TaskID        string          `json:"task_id"`
	Sequence      int64           `json:"sequence"`
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	Actor         string          `json:"actor"`
	RequestID     string          `json:"request_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    time.Time       `json:"occurred_at"`
}
type EventPage struct {
	Items     []Event `json:"items"`
	NextAfter int64   `json:"next_after"`
	HasMore   bool    `json:"has_more"`
}
type Filter struct {
	ProjectID, State, Cursor string
	Limit                    int
}
type Page struct {
	Items      []Task  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type Recent struct {
	ID          string `db:"id" json:"id"`
	ProjectID   string `db:"project_id" json:"project_id"`
	ProjectName string `db:"project_name" json:"project_name"`
	Title       string `db:"title" json:"title"`
	State       string `db:"state" json:"state"`
}
type Request struct {
	Action   string          `json:"action"`
	TaskID   string          `json:"task_id"`
	Response json.RawMessage `json:"response"`
	Status   int             `json:"status"`
}

type PlanVersion struct {
	ID        string    `json:"id" db:"id"`
	Revision  int64     `json:"revision" db:"revision"`
	ParentID  *string   `json:"parent_id" db:"parent_id"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
