package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/sessiontools"
	"github.com/wio-platform/wio/internal/task"
)

func (s *Store) AppendTaskPlan(ctx context.Context, project, id string, in task.AppendPlan) (task.PlanResult, error) {
	var result task.PlanResult
	err := s.taskCommand(ctx, project, id, "append_plan", in.Meta, in, &result, func(tx *sqlx.Tx) error {
		t, err := s.editableTaskTx(ctx, tx, project, id, in.ExpectedVersion)
		if err != nil {
			return err
		}
		proposal, err := s.validateTaskProposalTx(ctx, tx, t, in)
		if err != nil {
			return err
		}
		if err := task.ValidatePlan(in, t.Criteria); err != nil {
			return err
		}
		planLimit := 10
		if t.CurrentGrantRevision != nil {
			g, err := s.taskGrantTx(ctx, tx, id, *t.CurrentGrantRevision)
			if err != nil {
				return err
			}
			planLimit = g.Budgets.PlanRevisions
		}
		if t.Counters.Plans >= planLimit {
			return task.Fail(task.StateConflict, "task plan revision budget is exhausted")
		}
		if (in.ParentID == nil) != (t.CurrentPlanID == nil) || (in.ParentID != nil && *in.ParentID != *t.CurrentPlanID) {
			return task.Fail(task.Conflict, "parent_id must match the current plan")
		}
		workspaces := make([]string, 0, len(in.Members))
		for _, m := range in.Members {
			workspaces = append(workspaces, m.WorkspaceID)
		}
		sort.Strings(workspaces)
		for _, workspace := range workspaces {
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=? AND project_id=?"), workspace, project); err != nil {
				return err
			}
		}
		if err := s.validatePlanMembersTx(ctx, tx, project, in.Members); err != nil {
			return err
		}
		p := task.Plan{ID: NewID(), TaskID: id, Revision: int64(t.Counters.Plans + 1), ParentID: in.ParentID, SpecVersion: t.SpecVersion, Requirement: t.Requirement, Criteria: t.Criteria, Members: in.Members, Steps: in.Steps, GrantRevision: t.CurrentGrantRevision, Reason: in.Reason, Author: in.Actor, CreatedAt: time.Now().UTC()}
		// Only semantic content enters the digest; generated identity and time do not.
		content, err := json.Marshal(struct {
			SpecVersion int64
			Requirement string
			Criteria    []task.Criterion
			Members     []task.Member
			Steps       []task.Step
			Reason      string
			ParentID    *string
		}{p.SpecVersion, p.Requirement, p.Criteria, p.Members, p.Steps, p.Reason, p.ParentID})
		if err != nil {
			return err
		}
		p.ContentHash = HashToken(string(content))
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO task_plan_revisions(id,task_id,revision,parent_id,spec_version,content_json,content_hash,author,created_at) VALUES(?,?,?,?,?,?,?,?,?)"), p.ID, id, p.Revision, p.ParentID, p.SpecVersion, string(raw), p.ContentHash, p.Author, p.CreatedAt); err != nil {
			return err
		}
		for _, m := range p.Members {
			encoded, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_plan_members(task_id,plan_id,id,role,engine,workspace_id,execution_json) VALUES(?,?,?,?,?,?,?)"), id, p.ID, m.ID, m.Role, m.Engine, m.WorkspaceID, string(encoded)); err != nil {
				return err
			}
		}
		for _, step := range p.Steps {
			encoded, err := json.Marshal(step)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_plan_steps(task_id,plan_id,id,member_id,definition_json) VALUES(?,?,?,?,?)"), id, p.ID, step.ID, step.MemberID, string(encoded)); err != nil {
				return err
			}
		}
		for _, step := range p.Steps {
			for _, dep := range step.DependsOn {
				if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_step_dependencies(plan_id,step_id,depends_on) VALUES(?,?,?)"), p.ID, step.ID, dep); err != nil {
					return err
				}
			}
		}
		t.Counters.Plans++
		counters, err := json.Marshal(t.Counters)
		if err != nil {
			return err
		}
		changed, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET current_plan_id=?,counters_json=?,version=version+1,updated_at=? WHERE id=? AND project_id=? AND version=?"), p.ID, string(counters), p.CreatedAt, id, project, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := taskChanged(changed); err != nil {
			return err
		}
		result = task.PlanResult{Plan: p, TaskVersion: t.Version + 1}
		evidence := map[string]any{"plan_id": p.ID, "content_hash": p.ContentHash, "version": result.TaskVersion}
		if t.CurrentPlanID != nil {
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_team_requests SET state='superseded' WHERE task_id=? AND plan_id=? AND state='pending' AND id<>?"), id, *t.CurrentPlanID, proposal); err != nil {
				return err
			}
		}
		if proposal != "" {
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_team_requests SET state='adopted' WHERE id=?"), proposal); err != nil {
				return err
			}
			var invalidated []string
			if err := tx.SelectContext(ctx, &invalidated, s.Q("SELECT id FROM task_attempts WHERE run_id=? ORDER BY id"), *t.CurrentRunID); err != nil {
				return err
			}
			evidence["proposal_id"], evidence["previous_run_id"], evidence["invalidated_attempt_ids"] = proposal, *t.CurrentRunID, invalidated
		}
		return s.taskEventTx(ctx, tx, id, "task.plan.created", in.Meta, evidence)
	})
	return result, err
}

func (s *Store) taskPlanTx(ctx context.Context, tx *sqlx.Tx, id, plan string) (task.Plan, error) {
	var raw string
	var p task.Plan
	if err := tx.GetContext(ctx, &raw, s.Q("SELECT content_json FROM task_plan_revisions WHERE task_id=? AND id=?"), id, plan); err != nil {
		return p, err
	}
	err := json.Unmarshal([]byte(raw), &p)
	return p, err
}

// TaskSourceGuard protects immutable task inputs and historical execution
// evidence. It must run inside the deletion transaction, before any cleanup.
func (s *Store) taskSessionGuard(ctx context.Context, tx *sqlx.Tx, engine, id string) error {
	var count int
	if err := tx.GetContext(ctx, &count, s.Q(`SELECT
	 (SELECT COUNT(*) FROM task_attempts WHERE engine=? AND session_id=?) +
	 (SELECT COUNT(*) FROM task_session_bindings WHERE engine=? AND session_id=?)`), engine, id, engine, id); err != nil {
		return err
	}
	if count > 0 {
		return stateConflict("session is retained as task evidence; archive it instead")
	}
	return nil
}

func (s *Store) taskWorkspaceReferences(ctx context.Context, q deletionQueryer, id string) (int, error) {
	var count int
	err := q.GetContext(ctx, &count, s.Q(`SELECT
	 (SELECT COUNT(*) FROM task_plan_members WHERE workspace_id=?) +
	 (SELECT COUNT(*) FROM task_attempts WHERE workspace_id=?) +
     (SELECT COUNT(*) FROM task_artifacts WHERE workspace_id=?) +
     (SELECT COUNT(*) FROM task_artifact_handoffs WHERE workspace_id=?)`), id, id, id, id)
	return count, err
}

func (s *Store) validatePlanMembersTx(ctx context.Context, tx *sqlx.Tx, project string, members []task.Member) error {
	for _, m := range members {
		var w Workspace
		if err := tx.GetContext(ctx, &w, s.Q("SELECT w.* FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.project_id=? AND w.status='ready' AND s.revoked_at IS NULL"), m.WorkspaceID, project); err != nil {
			return err
		}
		if m.DirectoryMode == "existing_worktree" && w.Kind != "worktree" {
			return task.Fail(task.Invalid, "member workspace is not a worktree")
		}
		if !validCollaborationMode(m.Engine, m.Mode) {
			return task.Fail(task.Invalid, "invalid member execution mode")
		}
		if _, _, err := collaborationModelOverride(m.Engine, m.Model, m.Effort, sessiontools.Params{Model: m.Model, Effort: m.Effort}); err != nil {
			return task.Fail(task.Invalid, err.Error())
		}
	}
	return nil
}
