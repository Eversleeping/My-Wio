package task

import (
	"time"
)

type TeamPlan struct {
	Members []Member `json:"members"`
	Steps   []Step   `json:"steps"`
}

type TeamRequest struct {
	ID              string    `json:"id" db:"id"`
	TaskID          string    `json:"task_id" db:"task_id"`
	RunID           string    `json:"run_id" db:"run_id"`
	PlanID          string    `json:"plan_id" db:"plan_id"`
	SourceAttemptID string    `json:"source_attempt_id" db:"source_attempt_id"`
	SourceMemberID  string    `json:"source_member_id" db:"source_member_id"`
	RequestID       string    `json:"request_id" db:"request_id"`
	Kind            string    `json:"kind" db:"kind"`
	TargetMemberID  *string   `json:"target_member_id" db:"target_member_id"`
	TargetStepID    *string   `json:"target_step_id" db:"target_step_id"`
	ProducerAttempt *string   `json:"producer_attempt_id" db:"producer_attempt_id"`
	ArtifactID      *string   `json:"artifact_id" db:"artifact_id"`
	Reason          string    `json:"reason" db:"reason"`
	State           string    `json:"state" db:"state"`
	NeedsGrant      bool      `json:"requires_authorization" db:"requires_authorization"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
	Proposal        *TeamPlan `json:"proposal,omitempty" db:"-"`
}

type TeamToolInput struct {
	RequestID      string    `json:"request_id,omitempty"`
	TargetMemberID string    `json:"target_member_id,omitempty"`
	TargetStepID   string    `json:"target_step_id,omitempty"`
	ArtifactID     string    `json:"artifact_id,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Plan           *TeamPlan `json:"plan,omitempty"`
}
