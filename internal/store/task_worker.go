package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/task"
)

// AdvanceTasks is restartable: database attempts and operations carry all work
// identity. A worker never reconstructs an execution by allocating another ID.
func (s *Store) AdvanceTasks(ctx context.Context, owner string) error {
	var attempts []string
	if err := s.DB.SelectContext(ctx, &attempts, "SELECT id FROM task_attempts WHERE state IN ('queued','running','unknown') ORDER BY lease_expires_at,created_at,id LIMIT 100"); err != nil {
		return err
	}
	for _, id := range attempts {
		f, ok, err := s.ClaimTaskAttempt(ctx, id, owner)
		if err != nil {
			return err
		}
		if ok {
			if err := s.ReconcileTaskAttempt(ctx, f); err != nil {
				return err
			}
		}
	}
	var runs []struct {
		Task string `db:"task_id"`
		Run  string `db:"id"`
	}
	if err := s.DB.SelectContext(ctx, &runs, "SELECT task_id,id FROM task_runs WHERE state IN ('running','blocked','paused','cancelling') ORDER BY started_at,id"); err != nil {
		return err
	}
	for _, r := range runs {
		if err := s.projectTaskRun(ctx, r.Task, r.Run); err != nil {
			return err
		}
		var steps []string
		if err := s.DB.SelectContext(ctx, &steps, s.Q("SELECT step_id FROM task_step_runs WHERE run_id=? AND state IN ('pending','ready','blocked') AND current_attempt_id IS NULL ORDER BY step_id"), r.Run); err != nil {
			return err
		}
		for _, step := range steps {
			if _, err := s.DispatchTaskStep(ctx, r.Task, r.Run, step, owner); err != nil {
				return err
			}
		}
		if err := s.projectTaskRun(ctx, r.Task, r.Run); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) projectTaskRun(ctx context.Context, id, run string) error {
	return s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		if err := s.lockTaskTx(ctx, tx, id); err != nil {
			return err
		}
		r, err := s.taskRunTx(ctx, tx, id, run)
		if err != nil {
			return err
		}
		if !taskRunActive(r.State) {
			return nil
		}
		var active, remaining, exhausted int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND state IN ('queued','running','unknown')"), run); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &remaining, s.Q("SELECT COUNT(*) FROM task_step_runs WHERE run_id=? AND state<>'succeeded'"), run); err != nil {
			return err
		}
		var project string
		if err := tx.GetContext(ctx, &project, s.Q("SELECT project_id FROM tasks WHERE id=?"), id); err != nil {
			return err
		}
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		now, err := taskDBTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		next := r.State
		if r.State != "cancelling" && r.DeadlineAt != nil && !r.DeadlineAt.After(now) {
			next = "cancelling"
		}
		if t.CurrentGrantRevision != nil {
			g, err := s.taskGrantTx(ctx, tx, id, *t.CurrentGrantRevision)
			if err != nil {
				return err
			}
			if !g.Enabled {
				next = "cancelling"
			}
			if err := tx.GetContext(ctx, &exhausted, s.Q("SELECT COUNT(*) FROM task_step_runs st WHERE st.run_id=? AND st.state<>'succeeded' AND COALESCE(st.block_code,'') NOT IN ('artifact_required','review_required','review_rejected','review_inconclusive') AND (SELECT COUNT(*) FROM task_attempts a WHERE a.run_id=st.run_id AND a.step_id=st.step_id)>=?"), run, g.Budgets.StepAttempts); err != nil {
				return err
			}
		}
		if next == "cancelling" && active == 0 {
			next = "cancelled"
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='cancelled',version=version+1 WHERE run_id=? AND state<>'succeeded'"), run); err != nil {
				return err
			}
		} else if next != "cancelling" {
			if active == 0 && remaining == 0 {
				next = "awaiting_acceptance"
			} else if active == 0 && exhausted > 0 {
				next = "failed"
			} else if r.State != "paused" {
				if active > 0 {
					next = "running"
				} else {
					next = "blocked"
				}
			}
		}
		if next == r.State {
			return nil
		}
		var ended *time.Time
		if !taskRunActive(next) {
			ended = &now
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET released_at=?,active_attempt_id=NULL WHERE run_id=? AND released_at IS NULL"), now, run); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_runs SET state=?,version=version+1,ended_at=? WHERE id=?"), next, ended, run); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET version=version+1,updated_at=? WHERE id=?"), time.Now().UTC(), id); err != nil {
			return err
		}
		return s.taskEventTx(ctx, tx, id, "task.run.state.changed", task.Meta{Actor: "task:" + id}, map[string]any{"run_id": run, "state": next})
	})
}

func (s *Store) taskAttemptActionTx(ctx context.Context, tx *sqlx.Tx, t task.Task, r task.Run, in task.Action) error {
	if in.Action == "retry_step" {
		var row struct {
			State   string  `db:"state"`
			Attempt *string `db:"current_attempt_id"`
		}
		if err := tx.GetContext(ctx, &row, s.Q("SELECT state,current_attempt_id FROM task_step_runs WHERE run_id=? AND step_id=?"), r.ID, in.StepID); err != nil {
			return err
		}
		if row.State != "blocked" || row.Attempt == nil {
			return task.Fail(task.StateConflict, "step has no failed attempt to retry")
		}
		var state string
		if err := tx.GetContext(ctx, &state, s.Q("SELECT state FROM task_attempts WHERE id=?"), *row.Attempt); err != nil {
			return err
		}
		if state != "failed" && state != "cancelled" {
			return task.Fail(task.StateConflict, "confirm termination before retrying")
		}
		_, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='ready',current_attempt_id=NULL,block_code=NULL,version=version+1 WHERE run_id=? AND step_id=?"), r.ID, in.StepID)
		return err
	}
	var a task.Attempt
	if err := tx.GetContext(ctx, &a, s.Q(taskAttemptSelect+" WHERE id=? AND task_id=? AND run_id=?"), in.AttemptID, t.ID, r.ID); err != nil {
		return err
	}
	if in.Action == "takeover" {
		if a.State != "succeeded" && a.State != "failed" && a.State != "cancelled" {
			return task.Fail(task.StateConflict, "confirm the attempt stopped before taking over")
		}
		var active int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM task_attempts WHERE engine=? AND session_id=? AND state IN ('queued','running','unknown')"), a.Engine, a.SessionID); err != nil {
			return err
		}
		if active > 0 {
			return task.Fail(task.StateConflict, "a later attempt still owns this session")
		}
		_, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET released_at=?,active_attempt_id=NULL WHERE run_id=? AND engine=? AND session_id=? AND released_at IS NULL"), time.Now().UTC(), r.ID, a.Engine, a.SessionID)
		return err
	}
	if a.State != "unknown" || in.StoppedAt == nil || in.StoppedAt.IsZero() || in.StoppedAt.After(time.Now().UTC()) || len(in.Evidence) < 10 || len(in.Evidence) > 64<<10 || (in.Resolution != "failed" && in.Resolution != "cancelled") {
		return task.Fail(task.Invalid, "unknown resolution requires a stopped time, verification evidence and failed/cancelled outcome")
	}
	raw, _ := json.Marshal(in)
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_attempt_evidence(attempt_id,source_event_id,classification,payload_json,content_hash,acquired_at) VALUES(?,?,'user_stopped',?,?,?)"), a.ID, "user:"+in.Actor+":"+in.RequestID, string(raw), HashToken(string(raw)), time.Now().UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_attempts SET state=?,outcome_certainty='user_confirmed',ended_at=?,lease_generation=lease_generation+1 WHERE id=?"), in.Resolution, in.StoppedAt, a.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET active_attempt_id=NULL WHERE active_attempt_id=?"), a.ID); err != nil {
		return err
	}
	if a.OperationID != nil {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='cancelled',completed_at=?,result='Task execution stop verified by user' WHERE id=? AND status IN ('queued','delivered','running')"), time.Now().UTC(), *a.OperationID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET released_at=?,active_attempt_id=NULL WHERE run_id=? AND engine=? AND session_id=? AND released_at IS NULL"), time.Now().UTC(), r.ID, a.Engine, a.SessionID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='blocked',block_code='user_confirmed_stop',version=version+1 WHERE current_attempt_id=?"), a.ID)
	return err
}
