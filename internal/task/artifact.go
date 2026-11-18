package task

import "time"

type CaptureArtifact struct {
	Meta
	AttemptID string   `json:"attempt_id"`
	Paths     []string `json:"paths"`
}

type HandoffArtifact struct {
	Meta
	WorkspaceID string `json:"workspace_id"`
	Action      string `json:"action"`
	Digest      string `json:"digest"`
}

type ArtifactFileEvidence struct {
	Path       string `json:"path" db:"path"`
	BeforeHash string `json:"before_hash,omitempty" db:"before_hash"`
	BeforeMode string `json:"before_mode,omitempty" db:"before_mode"`
	AfterHash  string `json:"after_hash,omitempty" db:"after_hash"`
	AfterMode  string `json:"after_mode,omitempty" db:"after_mode"`
}

type Artifact struct {
	ID          string                 `json:"id" db:"id"`
	TaskID      string                 `json:"task_id" db:"task_id"`
	RunID       string                 `json:"run_id" db:"run_id"`
	AttemptID   string                 `json:"attempt_id" db:"attempt_id"`
	WorkspaceID string                 `json:"workspace_id" db:"workspace_id"`
	Revision    int64                  `json:"revision" db:"revision"`
	OperationID string                 `json:"operation_id" db:"operation_id"`
	State       string                 `json:"state" db:"state"`
	Digest      string                 `json:"digest" db:"manifest_hash"`
	BaseSHA     string                 `json:"base_sha" db:"base_sha"`
	Error       string                 `json:"error" db:"error"`
	CreatedAt   time.Time              `json:"created_at" db:"created_at"`
	Files       []ArtifactFileEvidence `json:"files" db:"-"`
}

type ArtifactHandoff struct {
	ID          string    `json:"id" db:"id"`
	TaskID      string    `json:"task_id" db:"task_id"`
	ArtifactID  string    `json:"artifact_id" db:"artifact_id"`
	WorkspaceID string    `json:"workspace_id" db:"workspace_id"`
	Action      string    `json:"action" db:"action"`
	OperationID string    `json:"operation_id" db:"operation_id"`
	State       string    `json:"state" db:"state"`
	Error       string    `json:"error" db:"error"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

type Artifacts struct {
	Items    []Artifact        `json:"items"`
	Handoffs []ArtifactHandoff `json:"handoffs"`
}
