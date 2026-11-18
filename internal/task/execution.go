package task

import (
	"bytes"
	"encoding/json"
	"time"
)

type Budgets struct {
	Concurrency   int `json:"concurrency"`
	Messages      int `json:"messages,omitempty"`
	Attempts      int `json:"attempts"`
	StepAttempts  int `json:"step_attempts"`
	Runs          int `json:"runs"`
	PlanRevisions int `json:"plan_revisions"`
	Reworks       int `json:"reworks,omitempty"`
	StepSeconds   int `json:"step_seconds"`
	RunSeconds    int `json:"run_seconds"`

	messagesSpecified bool
	reworksSpecified  bool
}

// Preserve default values for older requests and grants that omitted these
// budgets. An explicit zero disables the corresponding behavior.
func (b *Budgets) UnmarshalJSON(raw []byte) error {
	type fields Budgets
	var decoded struct {
		fields
		Messages *int `json:"messages"`
		Reworks  *int `json:"reworks"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*b = Budgets(decoded.fields)
	if decoded.Messages != nil {
		b.Messages, b.messagesSpecified = *decoded.Messages, true
	}
	if decoded.Reworks != nil {
		b.Reworks, b.reworksSpecified = *decoded.Reworks, true
	}
	return nil
}

func (b Budgets) MarshalJSON() ([]byte, error) {
	var messages, reworks *int
	if b.Messages != 0 || b.messagesSpecified {
		messages = &b.Messages
	}
	if b.Reworks != 0 || b.reworksSpecified {
		reworks = &b.Reworks
	}
	return json.Marshal(struct {
		Concurrency   int  `json:"concurrency"`
		Messages      *int `json:"messages,omitempty"`
		Attempts      int  `json:"attempts"`
		StepAttempts  int  `json:"step_attempts"`
		Runs          int  `json:"runs"`
		PlanRevisions int  `json:"plan_revisions"`
		Reworks       *int `json:"reworks,omitempty"`
		StepSeconds   int  `json:"step_seconds"`
		RunSeconds    int  `json:"run_seconds"`
	}{b.Concurrency, messages, b.Attempts, b.StepAttempts, b.Runs, b.PlanRevisions, reworks, b.StepSeconds, b.RunSeconds})
}

func DefaultBudgets() Budgets {
	return Budgets{Concurrency: 2, Attempts: 100, StepAttempts: 3, Runs: 10, PlanRevisions: 10, Reworks: 2, Messages: 100, StepSeconds: 3600, RunSeconds: 86400}
}
func ValidateBudgets(b Budgets) error {
	if b.Messages < 0 || b.Messages > 500 || b.Reworks < 0 || b.Reworks > 4 || b.Concurrency < 1 || b.Concurrency > 4 || b.Attempts < 1 || b.Attempts > 500 || b.StepAttempts < 1 || b.StepAttempts > 5 || b.Runs < 1 || b.Runs > 50 || b.PlanRevisions < 1 || b.PlanRevisions > 50 || b.StepSeconds < 1 || b.StepSeconds > 86400 || b.RunSeconds < 1 || b.RunSeconds > 604800 {
		return Fail(Invalid, "invalid task execution budgets")
	}
	return nil
}

func (b Budgets) MessageLimit() int {
	if b.Messages == 0 && !b.messagesSpecified {
		return 100
	}
	return b.Messages
}

func (b Budgets) ReworkLimit() int {
	if b.Reworks == 0 && !b.reworksSpecified {
		return 2
	}
	return b.Reworks
}

type ExecutionMode struct {
	Engine string `json:"engine"`
	Mode   string `json:"mode"`
}
type GrantInput struct {
	Meta
	TaskTools      bool                     `json:"task_tools,omitempty"`
	Enabled        bool                     `json:"enabled"`
	AllowedMembers []string                 `json:"allowed_members"`
	WorkspaceIDs   []string                 `json:"workspace_ids"`
	ExecutionModes map[string]ExecutionMode `json:"execution_modes"`
	Budgets        Budgets                  `json:"budgets"`
}
type Grant struct {
	TaskID         string                   `json:"task_id"`
	Revision       int64                    `json:"revision"`
	Actor          string                   `json:"actor"`
	TaskTools      bool                     `json:"task_tools,omitempty"`
	Enabled        bool                     `json:"enabled"`
	AllowedMembers []string                 `json:"allowed_members"`
	WorkspaceIDs   []string                 `json:"workspace_ids"`
	ExecutionModes map[string]ExecutionMode `json:"execution_modes"`
	Budgets        Budgets                  `json:"budgets"`
	CreatedAt      time.Time                `json:"created_at"`
}
type GrantResult struct {
	Grant       Grant `json:"grant"`
	TaskVersion int64 `json:"task_version"`
}
type Start struct {
	Meta
	PlanID        string `json:"plan_id"`
	GrantRevision int64  `json:"grant_revision"`
}
type RunResult struct {
	Run         Run    `json:"run"`
	TaskVersion int64  `json:"task_version"`
	RequestID   string `json:"request_id,omitempty"`
}
type Action struct {
	Meta
	Action     string     `json:"action"`
	Reason     string     `json:"reason"`
	StepID     string     `json:"step_id,omitempty"`
	AttemptID  string     `json:"attempt_id,omitempty"`
	Resolution string     `json:"resolution,omitempty"`
	Evidence   string     `json:"evidence,omitempty"`
	StoppedAt  *time.Time `json:"stopped_at,omitempty"`
}
type Fence struct {
	AttemptID  string
	Owner      string
	Generation int64
}
type Attempt struct {
	ID                   string     `json:"id" db:"id"`
	TaskID               string     `json:"task_id" db:"task_id"`
	RunID                string     `json:"run_id" db:"run_id"`
	PlanID               string     `json:"plan_id" db:"plan_id"`
	StepID               string     `json:"step_id" db:"step_id"`
	MemberID             string     `json:"member_id" db:"member_id"`
	Number               int        `json:"number" db:"number"`
	State                string     `json:"state" db:"state"`
	Engine               string     `json:"engine" db:"engine"`
	SessionID            string     `json:"session_id" db:"session_id"`
	ServerID             string     `json:"server_id" db:"server_id"`
	WorkspaceID          string     `json:"workspace_id" db:"workspace_id"`
	OperationID          *string    `json:"operation_id" db:"operation_id"`
	InterruptOperationID *string    `json:"interrupt_operation_id" db:"interrupt_operation_id"`
	LeaseOwner           *string    `json:"-" db:"lease_owner"`
	LeaseGeneration      int64      `json:"-" db:"lease_generation"`
	LeaseExpiresAt       *time.Time `json:"-" db:"lease_expires_at"`
	DeadlineAt           *time.Time `json:"deadline_at" db:"deadline_at"`
	EvidenceCursor       int64      `json:"evidence_cursor" db:"evidence_cursor"`
	OutcomeCertainty     string     `json:"outcome_certainty" db:"outcome_certainty"`
	ResultSummary        *string    `json:"result_summary" db:"result_summary"`
}
