package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wio-platform/wio/internal/session"
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
	var failures error
	for _, id := range attempts {
		f, ok, err := s.ClaimTaskAttempt(ctx, id, owner)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("claim task attempt %s: %w", id, err))
			continue
		}
		if ok {
			if err := s.ReconcileTaskAttempt(ctx, f); err != nil {
				failures = errors.Join(failures, fmt.Errorf("reconcile task attempt %s: %w", id, err))
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
			failures = errors.Join(failures, fmt.Errorf("project task run %s: %w", r.Run, err))
			continue
		}
		var steps []string
		if err := s.DB.SelectContext(ctx, &steps, s.Q("SELECT step_id FROM task_step_runs WHERE run_id=? AND state IN ('pending','ready','blocked') AND current_attempt_id IS NULL ORDER BY step_id"), r.Run); err != nil {
			failures = errors.Join(failures, fmt.Errorf("list steps for task run %s: %w", r.Run, err))
			continue
		}
		for _, step := range steps {
			if _, err := s.DispatchTaskStep(ctx, r.Task, r.Run, step, owner); err != nil {
				failures = errors.Join(failures, fmt.Errorf("dispatch task run %s step %s: %w", r.Run, step, err))
			}
		}
		if err := s.projectTaskRun(ctx, r.Task, r.Run); err != nil {
			failures = errors.Join(failures, fmt.Errorf("project task run %s: %w", r.Run, err))
			continue
		}
	}
	return failures
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
		var active, remaining int
		var exhausted []taskStepExhaustion
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
			if active == 0 && remaining > 0 && next != "cancelling" {
				exhausted, err = s.taskExhaustedStepsTx(ctx, tx, t, r, g.Budgets)
				if err != nil {
					return err
				}
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
			} else if len(exhausted) > 0 {
				next = "failed"
				for _, step := range exhausted {
					if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='failed',block_code=?,version=version+1 WHERE run_id=? AND step_id=?"), step.Code, run, step.ID); err != nil {
						return err
					}
				}
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
			Code    string  `db:"code"`
			Attempt *string `db:"current_attempt_id"`
		}
		if err := tx.GetContext(ctx, &row, s.Q("SELECT state,current_attempt_id,COALESCE(block_code,'') AS code FROM task_step_runs WHERE run_id=? AND step_id=?"), r.ID, in.StepID); err != nil {
			return err
		}
		if row.State != "blocked" || row.Attempt == nil {
			return task.Fail(task.StateConflict, "step has no failed attempt to retry")
		}
		var state string
		if err := tx.GetContext(ctx, &state, s.Q("SELECT state FROM task_attempts WHERE id=?"), *row.Attempt); err != nil {
			return err
		}
		if state != "failed" && state != "cancelled" && !(row.Code == "artifact_required" && state == "succeeded") {
			return task.Fail(task.StateConflict, "confirm termination before retrying")
		}
		if t.CurrentGrantRevision == nil {
			return task.Fail(task.Forbidden, "execution authorization is required")
		}
		grant, err := s.taskGrantTx(ctx, tx, t.ID, *t.CurrentGrantRevision)
		if err != nil {
			return err
		}
		var attempts int
		if err := tx.GetContext(ctx, &attempts, s.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND step_id=?"), r.ID, in.StepID); err != nil {
			return err
		}
		if attempts >= grant.Budgets.StepAttempts || t.Counters.Attempts >= grant.Budgets.Attempts {
			return task.Fail(task.StateConflict, "attempt budget is exhausted")
		}
		_, err = tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='ready',current_attempt_id=NULL,block_code=NULL,version=version+1 WHERE run_id=? AND step_id=?"), r.ID, in.StepID)
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
	for _, operation := range []*string{a.OperationID, a.InterruptOperationID} {
		if operation == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='cancelled',completed_at=?,result='Task execution stop verified by user' WHERE id=? AND status IN ('queued','delivered','running')"), time.Now().UTC(), *operation); err != nil {
			return err
		}
	}
	if err := s.markTaskSessionInterruptedTx(ctx, tx, a); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET released_at=?,active_attempt_id=NULL WHERE run_id=? AND engine=? AND session_id=? AND released_at IS NULL"), time.Now().UTC(), r.ID, a.Engine, a.SessionID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state='blocked',block_code='user_confirmed_stop',version=version+1 WHERE current_attempt_id=?"), a.ID)
	return err
}

type taskStepExhaustion struct {
	ID   string
	Code string
}

func (s *Store) taskExhaustedStepsTx(ctx context.Context, tx *sqlx.Tx, t task.Task, r task.Run, budgets task.Budgets) ([]taskStepExhaustion, error) {
	var steps []struct {
		ID       string `db:"step_id"`
		State    string `db:"state"`
		Code     string `db:"code"`
		Attempts int    `db:"attempts"`
		Reworks  int    `db:"reworks"`
	}
	if err := tx.SelectContext(ctx, &steps, s.Q(`SELECT st.step_id,st.state,COALESCE(st.block_code,'') AS code,st.reworks,COUNT(a.id) AS attempts
		FROM task_step_runs st LEFT JOIN task_attempts a ON a.run_id=st.run_id AND a.step_id=st.step_id
		WHERE st.run_id=? GROUP BY st.step_id,st.state,st.block_code,st.reworks`), r.ID); err != nil {
		return nil, err
	}
	counts, reworks := map[string]int{}, map[string]int{}
	for _, step := range steps {
		counts[step.ID], reworks[step.ID] = step.Attempts, step.Reworks
	}
	var plan *task.Plan
	var exhausted []taskStepExhaustion
	for _, step := range steps {
		if step.State == "succeeded" {
			continue
		}
		code := ""
		switch step.Code {
		case "artifact_required", "review_required":
			// Finishing this step only needs evidence or a decision.
		case "review_rejected", "review_inconclusive":
			if plan == nil {
				p, err := s.taskPlanTx(ctx, tx, t.ID, r.PlanID)
				if err != nil {
					return nil, err
				}
				plan = &p
			}
			root := step.ID
			if step.Code == "review_rejected" {
				root = taskReviewSource(findTaskStep(*plan, step.ID))
			}
			affected := taskDescendants(*plan, root)
			switch {
			case reworks[root] >= budgets.ReworkLimit():
				code = step.Code
			case t.Counters.Attempts+len(affected) > budgets.Attempts:
				code = "attempt_budget"
			default:
				for _, id := range affected {
					if counts[id] >= budgets.StepAttempts {
						code = "step_attempt_budget"
						break
					}
				}
			}
		default:
			switch {
			case t.Counters.Attempts >= budgets.Attempts:
				code = "attempt_budget"
			case step.Attempts >= budgets.StepAttempts:
				code = "step_attempt_budget"
			}
		}
		if code != "" {
			exhausted = append(exhausted, taskStepExhaustion{step.ID, code})
		}
	}
	return exhausted, nil
}

func (s *Store) markTaskSessionInterruptedTx(ctx context.Context, tx *sqlx.Tx, a task.Attempt) error {
	if a.OperationID == nil {
		return nil
	}
	query := ""
	switch a.Engine {
	case session.Codex:
		query = `UPDATE codex_threads SET status='interrupted',updated_at=? WHERE id=?
			AND status IN ('queued','running') AND ?=(SELECT id FROM agent_operations o WHERE o.thread_id=codex_threads.id AND o.kind IN ('codex.turn.start','codex.turn.rewrite') ORDER BY o.created_at DESC LIMIT 1)`
	case session.Claude, session.Gemini:
		query = "UPDATE " + a.Engine + "_threads SET status='interrupted',updated_at=? WHERE id=? AND operation_id=? AND status IN ('queued','running','interrupting')"
	default:
		return nil
	}
	_, err := tx.ExecContext(ctx, s.Q(query), time.Now().UTC(), a.SessionID, *a.OperationID)
	return err
}
