package protocol

import (
	"errors"
	"github.com/google/uuid"
	"regexp"
	"strings"
)

const ClaudeOwnershipLostMessage = "Agent no longer owns this Claude task; verify the old CLI process before recovering the session"

type ClaudeSkillsCommand struct {
	Workspace string `json:"workspace"`
}

type ClaudeSkill struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argument_hint,omitempty"`
}

type ClaudeSkillsResult struct {
	Skills []ClaudeSkill `json:"skills"`
}

var claudeSkillNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:/-]*$`)

func ValidClaudeSkillName(name string) bool {
	return len(name) <= 128 && claudeSkillNamePattern.MatchString(name)
}

// ClaudeTurnCommand contains no credentials. The CLI authenticates as the Agent account.
type ClaudeTurnCommand struct {
	Steer          bool        `json:"steer,omitempty"`
	Images         []TurnImage `json:"images,omitempty"`
	ForkSnapshotID string      `json:"fork_snapshot_id,omitempty"`
	ForkSessionID  string      `json:"fork_session_id,omitempty"`
	ResumePath     string      `json:"-"`
	ThreadID       string      `json:"thread_id"`
	Workspace      string      `json:"workspace"`
	SessionID      string      `json:"session_id"`
	Prompt         string      `json:"prompt"`
	Model          string      `json:"model"`
	PermissionMode string      `json:"permission_mode"`
	Effort         string      `json:"effort,omitempty"`
}

// A fork snapshots the transcript without starting a model turn. The first
// target turn uses the documented --resume <path> --fork-session interface.
type ClaudeForkCommand struct {
	SourceThreadID  string `json:"source_thread_id"`
	TargetThreadID  string `json:"target_thread_id"`
	SessionID       string `json:"session_id"`
	SourceWorkspace string `json:"source_workspace"`
	WorkspaceID     string `json:"workspace_id"`
	Workspace       string `json:"workspace"`
	Title           string `json:"title"`
	CutoffSequence  int64  `json:"cutoff_sequence"`
}
type ClaudeForkResult struct {
	SnapshotID string `json:"snapshot_id"`
}

func ValidClaudeEffort(effort string) bool {
	return effort == "" || effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh" || effort == "max"
}

type ClaudeInterruptCommand struct {
	ThreadID        string `json:"thread_id"`
	OperationID     string `json:"operation_id"`
	RecoveryPending bool   `json:"recovery_pending,omitempty"`
}

const ClaudeSteerUnconfirmedMessage = "Claude did not confirm the instruction; check its delivery status before sending it again"

type ClaudeSteerCommand struct {
	ThreadID    string      `json:"thread_id"`
	OperationID string      `json:"operation_id"`
	RequestID   string      `json:"request_id"`
	Prompt      string      `json:"prompt"`
	Images      []TurnImage `json:"images,omitempty"`
}

func ValidateClaudeSteer(c ClaudeSteerCommand) error {
	_, err := uuid.Parse(c.RequestID)
	if c.ThreadID == "" || c.OperationID == "" || err != nil || c.Prompt != strings.TrimSpace(c.Prompt) || len(c.Prompt) > 8192 || (c.Prompt == "" && len(c.Images) == 0) || ValidateGeminiImages(c.Images) != nil {
		return errors.New("invalid Claude steering instruction")
	}
	return nil
}
