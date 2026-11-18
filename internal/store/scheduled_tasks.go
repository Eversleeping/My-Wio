package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

type ScheduledTask = session.ScheduledTask

type ScheduledTaskInput = session.ScheduledTaskInput

// Scheduled-task history uses the shared session source, but retains its own
// server and the global project name (including revoked/offline servers).
var scheduledTaskSelect = `SELECT st.engine AS agent_kind,CASE WHEN st.engine='gemini' THEN st.thinking_level ELSE '' END AS thinking_level,st.id,st.thread_id,t.title thread_title,t.workspace_id,t.project_id,p.name project_name,
 st.server_id,s.name server_name,CASE WHEN s.revoked_at IS NULL AND s.last_seen_at>? THEN 'online' ELSE 'offline' END server_status,t.native_id AS codex_thread_id,t.path workspace_path,
 st.name,st.prompt,st.schedule,st.timezone,st.enabled,st.model,st.reasoning_effort,st.approval_mode,
 st.next_run_at,st.last_run_at,st.last_run_status,st.last_run_message,st.last_operation_id,st.created_at,st.updated_at
 FROM scheduled_tasks st JOIN (` + aiSessionRows + `) t ON t.id=st.thread_id AND t.engine=st.engine
 JOIN projects p ON p.id=t.project_id JOIN servers s ON s.id=st.server_id`

func scheduledTaskArgs(args ...any) []any {
	return append([]any{time.Now().UTC().Add(-ServerOnlineGracePeriod)}, args...)
}

func scheduledEngine(kind string) string {
	if kind == "" {
		return "codex"
	}
	return kind
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
	err = s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		if err := s.lockScheduledThread(ctx, tx, input.AgentKind, threadID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.Q(`INSERT INTO scheduled_tasks
		(engine,id,thread_id,server_id,name,prompt,schedule,timezone,enabled,model,reasoning_effort,approval_mode,thinking_level,next_run_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), scheduledEngine(input.AgentKind), id, threadID, serverID, input.Name, input.Prompt, input.Schedule, input.Timezone, boolInt(input.Enabled), input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel, nextRunAt.UTC(), now, now)
		return err
	})
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
	err = s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		if err := s.lockScheduledThread(ctx, tx, input.AgentKind, threadID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, s.Q(`UPDATE scheduled_tasks SET thread_id=?,server_id=?,name=?,prompt=?,schedule=?,timezone=?,enabled=?,model=?,reasoning_effort=?,approval_mode=?,thinking_level=?,next_run_at=?,updated_at=? WHERE id=? AND engine=?`), threadID, serverID, input.Name, input.Prompt, input.Schedule, input.Timezone, boolInt(input.Enabled), input.Model, input.ReasoningEffort, input.ApprovalMode, input.ThinkingLevel, nextRunAt.UTC(), now, id, scheduledEngine(input.AgentKind))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
	if err != nil {
		return ScheduledTask{}, err
	}
	return s.ScheduledTask(ctx, id)
}

func (s *Store) DeleteScheduledTask(ctx context.Context, id string) error {
	_, err := s.ScheduledTask(ctx, id)
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, s.Q("DELETE FROM scheduled_tasks WHERE id=?"), id)
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
	_, err := s.ScheduledTask(ctx, id)
	if err != nil {
		return false, err
	}
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE scheduled_tasks SET next_run_at=?,updated_at=? WHERE id=? AND enabled=1 AND next_run_at<=?"), next.UTC(), time.Now().UTC(), id, expected.UTC().Add(time.Nanosecond))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) MarkScheduledTaskRun(ctx context.Context, id, status, message, operationID string, at time.Time) error {
	_, err := s.ScheduledTask(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, s.Q(`UPDATE scheduled_tasks SET last_run_at=?,last_run_status=?,last_run_message=?,last_operation_id=?,updated_at=? WHERE id=?`), at.UTC(), status, message, operationID, time.Now().UTC(), id)
	return err
}

// DisableScheduledTask turns off a due task that can never run, such as one
// whose schedule or time zone no longer parses. Left enabled, it would stay
// due forever, and enough of them would fill every scheduler batch. Like
// ClaimScheduledTask it only applies while the task is still due as read, so
// an edit that fixed the schedule in the meantime is kept.
func (s *Store) DisableScheduledTask(ctx context.Context, id string, expected time.Time, message string, at time.Time) (bool, error) {
	_, err := s.ScheduledTask(ctx, id)
	if err != nil {
		return false, err
	}
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE scheduled_tasks SET enabled=0,last_run_at=?,last_run_status='failed',last_run_message=?,last_operation_id='',updated_at=? WHERE id=? AND enabled=1 AND next_run_at<=?"), at.UTC(), message, time.Now().UTC(), id, expected.UTC().Add(time.Nanosecond))
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
	engine      string
	taskID      string
	at          time.Time
	operationID string
}

func (s *Store) recordScheduledRun(ctx context.Context, tx *sqlx.Tx, run *scheduledRun, operationID string) error {
	if run == nil {
		return nil
	}
	result, err := tx.ExecContext(ctx, s.Q("UPDATE scheduled_tasks SET last_run_at=?,last_run_status='queued',last_run_message='',last_operation_id=?,updated_at=? WHERE id=? AND engine=?"), run.at.UTC(), operationID, time.Now().UTC(), run.taskID, run.engine)
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

// QueueScheduledCodexTurn commits the session claim, operation, user event
// and scheduled run together, just as the Claude and Gemini queues do.
func (s *Store) QueueScheduledCodexTurn(ctx context.Context, taskID string, thread Thread, command protocol.StartTurnCommand, at time.Time) (string, error) {
	var op string
	err := s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		run := &scheduledRun{engine: "codex", taskID: taskID, at: at}
		id, _, err := s.queueCodexTurnTx(ctx, tx, thread, command, run)
		if err != nil {
			return err
		}
		op = id
		return nil
	})
	return op, err
}

func (s *Store) MarkScheduledTaskByOperation(ctx context.Context, operationID, status, message string) error {
	if strings.TrimSpace(operationID) == "" {
		return nil
	}
	if _, err := s.DB.ExecContext(ctx, s.Q("UPDATE scheduled_tasks SET last_run_status=?,last_run_message=?,updated_at=? WHERE last_operation_id=? AND last_run_status IN ('queued','running')"), status, message, time.Now().UTC(), operationID); err != nil {
		return err
	}

	return nil
}

func (s *Store) MarkScheduledTaskByThread(ctx context.Context, threadID, status, message string) error {
	if strings.TrimSpace(threadID) == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE scheduled_tasks SET last_run_status=?,last_run_message=?,updated_at=? WHERE engine='codex' AND thread_id=? AND last_run_status IN ('queued','running')`), status, message, time.Now().UTC(), threadID)
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
		if operation.Kind == protocol.OpCodexTurnRewrite {
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
	if event.Kind != protocol.EventCodexTurnCancelled {
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
	var taskOwned int
	if err := s.DB.GetContext(ctx, &taskOwned, s.Q("SELECT COUNT(*) FROM task_attempts a JOIN task_runs r ON r.id=a.run_id WHERE a.operation_id=? AND r.state IN ('running','paused','blocked','cancelling','awaiting_acceptance')"), id); err != nil {
		return false, err
	}
	if taskOwned > 0 {
		return false, stateConflict("cancel the task run instead of its native operation")
	}
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

// Parent deletions cascade through the native thread tables. Until sessions
// share a physical table, preserve that cleanup in the same parent transaction.
func (s *Store) pruneOrphanScheduledTasks(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE
 (engine='codex' AND NOT EXISTS(SELECT 1 FROM codex_threads t WHERE t.id=scheduled_tasks.thread_id)) OR
 (engine='gemini' AND NOT EXISTS(SELECT 1 FROM gemini_threads t WHERE t.id=scheduled_tasks.thread_id)) OR
 (engine='claude' AND NOT EXISTS(SELECT 1 FROM claude_threads t WHERE t.id=scheduled_tasks.thread_id))`)
	return err
}

func (s *Store) lockScheduledThread(ctx context.Context, tx *sqlx.Tx, engine, id string) error {
	// The engine was validated by scheduledThread; table names never come from SQL input.
	table := scheduledEngine(engine) + "_threads"
	result, err := tx.ExecContext(ctx, s.Q("UPDATE "+table+" SET id=id WHERE id=?"), id)
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
}
