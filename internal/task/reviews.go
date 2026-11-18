package task

import "time"

type ReviewCheck struct {
	CriterionID string `json:"criterion_id"`
	State       string `json:"state"`
	Evidence    string `json:"evidence"`
}
type ReviewDecision struct {
	Meta
	AttemptID    string        `json:"attempt_id"`
	ArtifactID   string        `json:"artifact_id"`
	EvidenceHash string        `json:"evidence_hash"`
	Verdict      string        `json:"verdict"`
	Reason       string        `json:"reason"`
	Checks       []ReviewCheck `json:"checks"`
}
type Review struct {
	AttemptID    string        `json:"attempt_id" db:"attempt_id"`
	TaskID       string        `json:"task_id" db:"task_id"`
	RunID        string        `json:"run_id" db:"run_id"`
	ArtifactID   string        `json:"artifact_id" db:"artifact_id"`
	Digest       string        `json:"artifact_digest" db:"artifact_digest"`
	StepID       string        `json:"step_id" db:"reviewer_step_id"`
	Verdict      *string       `json:"verdict" db:"verdict"`
	Reason       *string       `json:"reason" db:"reason"`
	DecidedBy    *string       `json:"decided_by" db:"decided_by"`
	DecidedAt    *time.Time    `json:"decided_at" db:"decided_at"`
	EvidenceHash *string       `json:"evidence_hash" db:"evidence_hash"`
	Criteria     []Criterion   `json:"criteria" db:"-"`
	Checks       []ReviewCheck `json:"checks" db:"-"`
}
