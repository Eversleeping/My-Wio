package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/task"
)

func taskContains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func (s *Store) taskGrantTx(ctx context.Context, tx *sqlx.Tx, id string, revision int64) (task.Grant, error) {
	var raw string
	var g task.Grant
	err := tx.GetContext(ctx, &raw, s.Q("SELECT grant_json FROM task_grant_revisions WHERE task_id=? AND revision=?"), id, revision)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &g)
	}
	return g, err
}
func validateTaskGrant(g task.Grant, p task.Plan) error {
	if !g.Enabled {
		return task.Fail(task.Forbidden, "task execution authorization is disabled")
	}
	for _, m := range p.Members {
		mode, ok := g.ExecutionModes[m.ID]
		if !taskContains(g.AllowedMembers, m.ID) || !taskContains(g.WorkspaceIDs, m.WorkspaceID) || !ok || mode.Engine != m.Engine || mode.Mode != m.Mode {
			return task.Fail(task.Forbidden, "plan exceeds the current task authorization")
		}
	}
	return task.ValidateBudgets(g.Budgets)
}
func (s *Store) SetTaskGrant(ctx context.Context, project, id string, in task.GrantInput) (task.GrantResult, error) {
	var out task.GrantResult
	err := s.taskCommand(ctx, project, id, "grant", in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if t.Version != in.ExpectedVersion {
			return task.Fail(task.Conflict, "task version changed")
		}
		if t.ArchivedAt != nil {
			return task.Fail(task.StateConflict, "restore the task before authorizing execution")
		}
		if err := task.ValidateBudgets(in.Budgets); err != nil {
			return err
		}
		if t.CurrentPlanID == nil {
			return task.Fail(task.StateConflict, "save a plan before authorizing execution")
		}
		p, err := s.taskPlanTx(ctx, tx, id, *t.CurrentPlanID)
		if err != nil {
			return err
		}
		revision := int64(1)
		if t.CurrentGrantRevision != nil {
			revision = *t.CurrentGrantRevision + 1
		}
		g := task.Grant{TaskID: id, Revision: revision, Actor: in.Actor, Enabled: in.Enabled, TaskTools: in.TaskTools, AllowedMembers: in.AllowedMembers, WorkspaceIDs: in.WorkspaceIDs, ExecutionModes: in.ExecutionModes, Budgets: in.Budgets, CreatedAt: time.Now().UTC()}
		// Validate every supplied scope, even disabled grants; unknown references
		// must never become an authorization that can later be enabled.
		members := map[string]bool{}
		for _, m := range p.Members {
			members[m.ID] = true
		}
		seen := map[string]bool{}
		for _, m := range g.AllowedMembers {
			if !members[m] || seen[m] {
				return task.Fail(task.Invalid, "invalid authorized member")
			}
			seen[m] = true
		}
		for m, mode := range g.ExecutionModes {
			if !members[m] || !validCollaborationMode(mode.Engine, mode.Mode) {
				return task.Fail(task.Invalid, "invalid authorized execution mode")
			}
		}
		seen = map[string]bool{}
		for _, w := range g.WorkspaceIDs {
			var found string
			if seen[w] {
				return task.Fail(task.Invalid, "duplicate authorized workspace")
			}
			seen[w] = true
			if err := tx.GetContext(ctx, &found, s.Q("SELECT id FROM workspaces WHERE id=? AND project_id=?"), w, project); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_grant_revisions(task_id,revision,actor,grant_json,created_at) VALUES(?,?,?,?,?)"), id, revision, in.Actor, string(raw), g.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET current_grant_revision=?,version=version+1,updated_at=? WHERE id=?"), revision, g.CreatedAt, id); err != nil {
			return err
		}
		if !g.Enabled {
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_runs SET state='cancelling',version=version+1 WHERE task_id=? AND state IN ('running','paused','blocked','awaiting_acceptance')"), id); err != nil {
				return err
			}
		}
		out = task.GrantResult{Grant: g, TaskVersion: t.Version + 1}
		return s.taskEventTx(ctx, tx, id, "task.grant.updated", in.Meta, map[string]any{"grant_revision": revision, "enabled": g.Enabled, "version": out.TaskVersion})
	})
	return out, err
}
func (s *Store) taskRunTx(ctx context.Context, tx *sqlx.Tx, id, run string) (task.Run, error) {
	var r struct {
		ID               string     `db:"id"`
		TaskID           string     `db:"task_id"`
		PlanID           string     `db:"plan_id"`
		State            string     `db:"state"`
		Version          int64      `db:"version"`
		Grant            int64      `db:"grant_revision"`
		Started          *time.Time `db:"started_at"`
		Deadline         *time.Time `db:"deadline_at"`
		Ended            *time.Time `db:"ended_at"`
		AcceptedBy       *string    `db:"accepted_by"`
		AcceptanceReason *string    `db:"acceptance_reason"`
		AcceptedAt       *time.Time `db:"accepted_at"`
	}
	err := tx.GetContext(ctx, &r, s.Q("SELECT id,task_id,plan_id,state,version,grant_revision,started_at,deadline_at,ended_at,accepted_by,acceptance_reason,accepted_at FROM task_runs WHERE task_id=? AND id=?"), id, run)
	return task.Run{ID: r.ID, TaskID: r.TaskID, PlanID: r.PlanID, State: r.State, Version: r.Version, GrantRevision: r.Grant, StartedAt: r.Started, DeadlineAt: r.Deadline, EndedAt: r.Ended, AcceptedBy: r.AcceptedBy, AcceptanceReason: r.AcceptanceReason, AcceptedAt: r.AcceptedAt}, err
}
func (s *Store) StartTaskRun(ctx context.Context, project, id string, in task.Start) (task.RunResult, error) {
	var out task.RunResult
	err := s.taskCommand(ctx, project, id, "start", in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.editableTaskTx(ctx, tx, project, id, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if t.CurrentPlanID == nil || *t.CurrentPlanID != in.PlanID {
			return task.Fail(task.Conflict, "choose the current plan")
		}
		p, err := s.taskPlanTx(ctx, tx, id, in.PlanID)
		if err != nil {
			return err
		}
		if p.SpecVersion != t.SpecVersion {
			return task.Fail(task.Conflict, "save a plan for the current specification")
		}
		if t.CurrentGrantRevision == nil || *t.CurrentGrantRevision != in.GrantRevision {
			return task.Fail(task.Forbidden, "confirm the current authorization")
		}
		g, err := s.taskGrantTx(ctx, tx, id, in.GrantRevision)
		if err != nil {
			return err
		}
		if err := validateTaskGrant(g, p); err != nil {
			return err
		}
		if t.Counters.Runs >= g.Budgets.Runs || t.Counters.Attempts >= g.Budgets.Attempts {
			return task.Fail(task.StateConflict, "task execution budget is exhausted")
		}
		if err := task.ValidatePlan(task.AppendPlan{Members: p.Members, Steps: p.Steps, Reason: p.Reason}, p.Criteria); err != nil {
			return err
		}
		for _, m := range p.Members {
			var w string
			if err := tx.GetContext(ctx, &w, s.Q("SELECT w.id FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.project_id=? AND w.status='ready' AND s.revoked_at IS NULL"), m.WorkspaceID, project); err != nil {
				return err
			}
		}
		run := NewID()
		now, err := taskDBTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		deadline := now.Add(time.Duration(g.Budgets.RunSeconds) * time.Second)
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_runs(id,task_id,plan_id,grant_revision,state,started_at,deadline_at) VALUES(?,?,?,?,'running',?,?)"), run, id, p.ID, g.Revision, now, deadline); err != nil {
			return err
		}
		for _, step := range p.Steps {
			state := "pending"
			if len(step.DependsOn) == 0 {
				state = "ready"
			}
			if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_step_runs(task_id,run_id,plan_id,step_id,state) VALUES(?,?,?,?,?)"), id, run, p.ID, step.ID, state); err != nil {
				return err
			}
		}
		t.Counters.Runs++
		raw, _ := json.Marshal(t.Counters)
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET current_run_id=?,counters_json=?,version=version+1,updated_at=? WHERE id=?"), run, string(raw), now, id); err != nil {
			return err
		}
		r, err := s.taskRunTx(ctx, tx, id, run)
		if err != nil {
			return err
		}
		out = task.RunResult{Run: r, TaskVersion: t.Version + 1}
		return s.taskEventTx(ctx, tx, id, "task.run.created", in.Meta, map[string]any{"run_id": run, "plan_id": p.ID, "grant_revision": g.Revision})
	})
	return out, err
}
func taskRunActive(state string) bool {
	return state == "running" || state == "paused" || state == "blocked" || state == "cancelling" || state == "awaiting_acceptance"
}
func (s *Store) ActOnTaskRun(ctx context.Context, project, id, run string, in task.Action) (task.RunResult, error) {
	var out task.RunResult
	err := s.taskCommand(ctx, project, id, "action:"+run, in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		r, err := s.taskRunTx(ctx, tx, id, run)
		if err != nil {
			return err
		}
		if r.Version != in.ExpectedVersion {
			return task.Fail(task.Conflict, "run version changed")
		}
		if !taskRunActive(r.State) || t.CurrentRunID == nil || *t.CurrentRunID != run {
			return task.Fail(task.StateConflict, "run is no longer active")
		}
		if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
			return task.Fail(task.Invalid, "provide a reason of at most 4 KiB")
		}
		next := r.State
		switch in.Action {
		case "pause":
			if r.State != "running" && r.State != "blocked" {
				return task.Fail(task.StateConflict, "run cannot be paused")
			}
			next = "paused"
		case "resume":
			if r.State != "paused" && r.State != "blocked" {
				return task.Fail(task.StateConflict, "run cannot be resumed")
			}
			if t.CurrentGrantRevision == nil {
				return task.Fail(task.Forbidden, "task has no authorization")
			}
			g, err := s.taskGrantTx(ctx, tx, id, *t.CurrentGrantRevision)
			if err != nil {
				return err
			}
			p, err := s.taskPlanTx(ctx, tx, id, r.PlanID)
			if err != nil {
				return err
			}
			if err := validateTaskGrant(g, p); err != nil {
				return err
			}
			next = "running"
		case "cancel":
			next = "cancelling"
		case "rework", "repeat_review":
			if err := s.taskReworkTx(ctx, tx, t, r, in); err != nil {
				return err
			}
		case "retry_step", "resolve_unknown", "takeover":
			if r.State == "awaiting_acceptance" || r.State == "cancelling" && in.Action != "resolve_unknown" {
				return task.Fail(task.StateConflict, "run does not allow attempt actions")
			}
			if err := s.taskAttemptActionTx(ctx, tx, t, r, in); err != nil {
				return err
			}
			if in.Action == "takeover" {
				next = "paused"
			}
		default:
			return task.Fail(task.Invalid, "invalid run action")
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_runs SET state=?,version=version+1 WHERE id=? AND task_id=?"), next, run, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET version=version+1,updated_at=? WHERE id=?"), time.Now().UTC(), id); err != nil {
			return err
		}
		r, err = s.taskRunTx(ctx, tx, id, run)
		if err != nil {
			return err
		}
		out = task.RunResult{Run: r, TaskVersion: t.Version + 1, RequestID: in.RequestID}
		return s.taskEventTx(ctx, tx, id, "task.run.state.changed", in.Meta, map[string]any{"run_id": run, "state": next, "action": in.Action, "reason": in.Reason, "step_id": in.StepID, "attempt_id": in.AttemptID, "resolution": in.Resolution})
	})
	return out, err
}
