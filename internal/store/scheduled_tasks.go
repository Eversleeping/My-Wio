package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

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

// server_status is derived from the last heartbeat like everywhere else:
// servers.status is only reset on a clean disconnect and stays "online" after
// a control-plane crash or a hung Agent connection. Every branch of
// scheduledTaskSelect takes the cutoff; see scheduledTaskArgs.
const codexScheduledTaskSelect = `SELECT 'codex' AS agent_kind,'' AS thinking_level,st.id,st.thread_id,t.title thread_title,t.workspace_id,w.project_id,p.name project_name,
	st.server_id,s.name server_name,CASE WHEN s.revoked_at IS NULL AND s.last_seen_at>? THEN 'online' ELSE 'offline' END server_status,t.codex_thread_id,w.path workspace_path,
	st.name,st.prompt,st.schedule,st.timezone,st.enabled,st.model,st.reasoning_effort,st.approval_mode,
	st.next_run_at,st.last_run_at,st.last_run_status,st.last_run_message,st.last_operation_id,st.created_at,st.updated_at
	FROM codex_scheduled_tasks st
	JOIN codex_threads t ON t.id=st.thread_id
	JOIN workspaces w ON w.id=t.workspace_id
	JOIN projects p ON p.id=w.project_id
	JOIN servers s ON s.id=st.server_id`

var geminiScheduledTaskSelect = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(codexScheduledTaskSelect, "'codex' AS agent_kind", "'gemini' AS agent_kind"), "codex_scheduled_tasks", "gemini_scheduled_tasks"), "JOIN codex_threads", "JOIN gemini_threads")
var claudeScheduledTaskSelect = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(codexScheduledTaskSelect, "'codex' AS agent_kind", "'claude' AS agent_kind"), "codex_scheduled_tasks", "claude_scheduled_tasks"), "JOIN codex_threads", "JOIN claude_threads")
var scheduledTaskSelect = "SELECT * FROM (" + codexScheduledTaskSelect + " UNION ALL " + strings.ReplaceAll(strings.ReplaceAll(geminiScheduledTaskSelect, "t.codex_thread_id", "t.session_id AS codex_thread_id"), "'' AS thinking_level", "st.thinking_level") + " UNION ALL " + strings.ReplaceAll(claudeScheduledTaskSelect, "t.codex_thread_id", "t.session_id AS codex_thread_id") + ") st"

// scheduledTaskArgs prepends the server heartbeat cutoff of each of the three
// branches of scheduledTaskSelect to the arguments of the outer query.
func scheduledTaskArgs(args ...any) []any {
	cutoff := time.Now().UTC().Add(-ServerOnlineGracePeriod)
	return append([]any{cutoff, cutoff, cutoff}, args...)
}

func scheduledTable(kind string) string {
	if kind == "claude" {
		return "claude_scheduled_tasks"
	}
	if kind == "gemini" {
		return "gemini_scheduled_tasks"
	}
	return "codex_scheduled_tasks"
}
func (s *Store) scheduledTableForID(ctx context.Context, id string) (string, error) {
	task, err := s.ScheduledTask(ctx, id)
	return scheduledTable(task.AgentKind), err
}
func (s *Store) scheduledThread(ctx context.Context, kind, id string) (string, string, error) {
	if kind == "claude" {
		t, e := s.ClaudeThread(ctx, id)
		return t.ID, t.ServerID, e
	}
	if kind == "gemini" {
		t, e := s.GeminiThread(ctx, id)
		return t.ID, t.ServerID, e
	}
	if kind != "" && kind != "codex" {
		return "", "", errors.New("invalid scheduled Agent kind")
	}
	t, e := s.Thread(ctx, id)
	return t.ID, t.ServerID, e
}

func (s *Store) ListScheduledTasks(ctx context.Context) ([]ScheduledTask, error) {
	rows := make([]ScheduledTask, 0)
	err := s.DB.SelectContext(ctx, &rows, s.Q(scheduledTaskSelect+" ORDER BY st.enabled DESC,st.next_run_at,st.updated_at DESC"), scheduledTaskArgs()...)
	return rows, err
}

func (s *Store) ScheduledTask(ctx context.Context, id string) (ScheduledTask, error) {
	var task ScheduledTask
	err := s.DB.GetContext(ctx, &task, s.Q(scheduledTaskSelect+" WHERE st.id=?"), scheduledTaskArgs(id)...)
	return task, err
}

func (s *Store) CreateScheduledTask(ctx context.Context, input ScheduledTaskInput, nextRunAt time.Time) (ScheduledTask, error) {
	threadID, serverID, err := s.scheduledThread(ctx, input.AgentKind, input.ThreadID)
	if err != nil {
		return ScheduledTask{}, err
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Prompt) == "" {
		return ScheduledTask{}, errors.New("scheduled task name and prompt are required")
	}
	id := NewID()
	now := time.Now().UTC()
	_, err = s.DB.ExecContext(ctx, s.Q(`INSERT INTO `+scheduledTable(input.AgentKind)+`
		(id,thread_id,server_id,name,prompt,schedule,timezone,enabled,model,reasoning_effort,approval_mode,thinking_level,next_run_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), id, threadID, serverID, input.Name, input.Prompt, input.Schedule, input.Timezone, boolInt(input.Enabled), input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel, nextRunAt.UTC(), now, now)
	if err != nil {
		return ScheduledTask{}, err
	}
	return s.ScheduledTask(ctx, id)
}

func (s *Store) UpdateScheduledTask(ctx context.Context, id string, input ScheduledTaskInput, nextRunAt time.Time) (ScheduledTask, error) {
	threadID, serverID, err := s.scheduledThread(ctx, input.AgentKind, input.ThreadID)
	if err != nil {
		return ScheduledTask{}, err
	}
	now := time.Now().UTC()
	result, err := s.DB.ExecContext(ctx, s.Q(`UPDATE `+scheduledTable(input.AgentKind)+` SET thread_id=?,server_id=?,name=?,prompt=?,schedule=?,timezone=?,enabled=?,model=?,reasoning_effort=?,approval_mode=?,thinking_level=?,next_run_at=?,updated_at=? WHERE id=?`), threadID, serverID, input.Name, input.Prompt, input.Schedule, input.Timezone, boolInt(input.Enabled), input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel, nextRunAt.UTC(), now, id)
	if err != nil {
		return ScheduledTask{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ScheduledTask{}, err
	}
	if changed == 0 {
		return ScheduledTask{}, sql.ErrNoRows
	}
	return s.ScheduledTask(ctx, id)
}

func (s *Store) DeleteScheduledTask(ctx context.Context, id string) error {
	table, err := s.scheduledTableForID(ctx, id)
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, s.Q("DELETE FROM "+table+" WHERE id=?"), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DueScheduledTasks(ctx context.Context, now time.Time, limit int) ([]ScheduledTask, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows := make([]ScheduledTask, 0)
	err := s.DB.SelectContext(ctx, &rows, s.Q(scheduledTaskSelect+" WHERE st.enabled=1 AND st.next_run_at<=? ORDER BY st.next_run_at LIMIT ?"), scheduledTaskArgs(now.UTC(), limit)...)
	return rows, err
}

// ClaimScheduledTask advances next_run_at only if the task is still due at the
// time it was read. This makes multiple control-plane workers harmless.
func (s *Store) ClaimScheduledTask(ctx context.Context, id string, expected, next time.Time) (bool, error) {
	table, err := s.scheduledTableForID(ctx, id)
	if err != nil {
		return false, err
	}
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE "+table+" SET next_run_at=?,updated_at=? WHERE id=? AND enabled=1 AND next_run_at<=?"), next.UTC(), time.Now().UTC(), id, expected.UTC().Add(time.Nanosecond))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) MarkScheduledTaskRun(ctx context.Context, id, status, message, operationID string, at time.Time) error {
	table, err := s.scheduledTableForID(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, s.Q(`UPDATE `+table+` SET last_run_at=?,last_run_status=?,last_run_message=?,last_operation_id=?,updated_at=? WHERE id=?`), at.UTC(), status, message, operationID, time.Now().UTC(), id)
	return err
}

// DisableScheduledTask turns off a due task that can never run, such as one
// whose schedule or time zone no longer parses. Left enabled, it would stay
// due forever, and enough of them would fill every scheduler batch. Like
// ClaimScheduledTask it only applies while the task is still due as read, so
// an edit that fixed the schedule in the meantime is kept.
func (s *Store) DisableScheduledTask(ctx context.Context, id string, expected time.Time, message string, at time.Time) (bool, error) {
	table, err := s.scheduledTableForID(ctx, id)
	if err != nil {
		return false, err
	}
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE "+table+" SET enabled=0,last_run_at=?,last_run_status='failed',last_run_message=?,last_operation_id='',updated_at=? WHERE id=? AND enabled=1 AND next_run_at<=?"), at.UTC(), message, time.Now().UTC(), id, expected.UTC().Add(time.Nanosecond))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// scheduledRun is the scheduled task on whose behalf a Claude or Gemini turn
// is queued. The queueing transaction records the run on the task, so the
// Agent's result can never arrive before the task references its operation.
type scheduledRun struct {
	table       string
	taskID      string
	at          time.Time
	operationID string
}

func (s *Store) recordScheduledRun(ctx context.Context, tx *sqlx.Tx, run *scheduledRun, operationID string) error {
	if run == nil {
		return nil
	}
	result, err := tx.ExecContext(ctx, s.Q("UPDATE "+run.table+" SET last_run_at=?,last_run_status='queued',last_run_message='',last_operation_id=?,updated_at=? WHERE id=?"), run.at.UTC(), operationID, time.Now().UTC(), run.taskID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		// The task was deleted after it was claimed; do not start its turn.
		return sql.ErrNoRows
	}
	run.operationID = operationID
	return nil
}

// MarkScheduledCodexRunQueued records a scheduled Codex run whose turn was
// queued in an earlier transaction. The Agent may already have reported the
// result, which MarkScheduledTaskByOperation could not attribute to this task
// yet. Locking the operation row makes a concurrent completion wait for this
// write; a result that already arrived is carried over instead of being
// overwritten with "queued".
func (s *Store) MarkScheduledCodexRunQueued(ctx context.Context, id, operationID string, at time.Time) error {
	return s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=status WHERE id=?"), operationID); err != nil {
			return err
		}
		var operation struct {
			Status string `db:"status"`
			Result string `db:"result"`
		}
		if err := tx.GetContext(ctx, &operation, s.Q("SELECT status,COALESCE(result,'') result FROM agent_operations WHERE id=?"), operationID); err != nil {
			return err
		}
		status, message := "queued", ""
		switch operation.Status {
		case "failed", "cancelled":
			status, message = operation.Status, operation.Result
		case "succeeded":
			// Codex reports success once the turn has started.
			status = "running"
		}
		result, err := tx.ExecContext(ctx, s.Q("UPDATE codex_scheduled_tasks SET last_run_at=?,last_run_status=?,last_run_message=?,last_operation_id=?,updated_at=? WHERE id=?"), at.UTC(), status, message, operationID, time.Now().UTC(), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return sql.ErrNoRows
		}
		return nil
	})
}

func (s *Store) MarkScheduledTaskByOperation(ctx context.Context, operationID, status, message string) error {
	if strings.TrimSpace(operationID) == "" {
		return nil
	}
	for _, table := range []string{"codex_scheduled_tasks", "gemini_scheduled_tasks", "claude_scheduled_tasks"} {
		if _, err := s.DB.ExecContext(ctx, s.Q("UPDATE "+table+" SET last_run_status=?,last_run_message=?,updated_at=? WHERE last_operation_id=? AND last_run_status IN ('queued','running')"), status, message, time.Now().UTC(), operationID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) MarkScheduledTaskByThread(ctx context.Context, threadID, status, message string) error {
	if strings.TrimSpace(threadID) == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE codex_scheduled_tasks SET last_run_status=?,last_run_message=?,updated_at=? WHERE thread_id=? AND last_run_status IN ('queued','running')`), status, message, time.Now().UTC(), threadID)
	return err
}

func (s *Store) ActiveCodexTurnOperation(ctx context.Context, threadID string) (Operation, error) {
	var operations []Operation
	if err := s.DB.SelectContext(ctx, &operations, s.Q(operationSelect+` WHERE kind IN ('codex.turn.start','codex.turn.rewrite') AND status IN ('queued','delivered','running') ORDER BY created_at DESC LIMIT 100`)); err != nil {
		return Operation{}, err
	}
	for _, operation := range operations {
		var command struct {
			ThreadID string `json:"thread_id"`
			Start    struct {
				ThreadID string `json:"thread_id"`
			} `json:"start"`
		}
		if json.Unmarshal([]byte(operation.Payload), &command) != nil {
			continue
		}
		candidate := command.ThreadID
		if operation.Kind == "codex.turn.rewrite" {
			candidate = command.Start.ThreadID
		}
		if candidate == threadID {
			return operation, nil
		}
	}
	return Operation{}, sql.ErrNoRows
}

// HasPendingTurnCancellation reports whether the latest control-plane turn
// lifecycle event cancelled a turn before it reached Codex. Late Agent events
// from that delivery must not make the session look active again. A new
// user.message event naturally clears the marker for the next turn.
func (s *Store) HasPendingTurnCancellation(ctx context.Context, threadID string) (bool, error) {
	var event struct {
		Kind    string `db:"kind"`
		Payload string `db:"payload"`
	}
	if err := s.DB.GetContext(ctx, &event, s.Q(`SELECT kind,payload FROM events
		WHERE stream_id=? AND kind IN ('user.message','codex.turn.cancelled')
		ORDER BY sequence DESC LIMIT 1`), threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if event.Kind != "codex.turn.cancelled" {
		return false, nil
	}
	var marker struct {
		Source string `json:"source"`
	}
	if json.Unmarshal([]byte(event.Payload), &marker) != nil {
		return false, nil
	}
	return marker.Source == "control", nil
}

func (s *Store) CancelOperation(ctx context.Context, id, message string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='cancelled',result=?,completed_at=? WHERE id=? AND status IN ('queued','delivered','running')"), message, time.Now().UTC(), id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
