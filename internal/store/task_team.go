package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
	"github.com/wio-platform/wio/internal/task"
)

type teamRow struct {
	task.TeamRequest
	ProposalJSON *string `db:"proposal_json"`
	InputHash    string  `db:"input_hash"`
}

func (r teamRow) request() (task.TeamRequest, error) {
	out := r.TeamRequest
	if r.ProposalJSON != nil {
		if err := json.Unmarshal([]byte(*r.ProposalJSON), &out.Proposal); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *Store) taskTeamRows(ctx context.Context, tx *sqlx.Tx, query string, args ...any) ([]task.TeamRequest, error) {
	var rows []teamRow
	if err := tx.SelectContext(ctx, &rows, s.Q("SELECT * FROM task_team_requests "+query), args...); err != nil {
		return nil, err
	}
	out := make([]task.TeamRequest, 0, len(rows))
	for _, row := range rows {
		request, err := row.request()
		if err != nil {
			return nil, err
		}
		out = append(out, request)
	}
	return out, nil
}

func (s *Store) TaskTeam(ctx context.Context, project, id string) ([]task.TeamRequest, error) {
	var out []task.TeamRequest
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := s.taskTx(ctx, tx, project, id); err != nil {
			return err
		}
		var err error
		out, err = s.taskTeamRows(ctx, tx, "WHERE task_id=? ORDER BY created_at DESC,id DESC", id)
		return err
	})
	return out, err
}

func (s *Store) ExecuteTaskTool(ctx context.Context, server string, call sessiontools.Call) sessiontools.Reply {
	var in task.TeamToolInput
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage(`{}`)
	}
	if len(call.Arguments) > 128<<10 {
		return collaborationError("", sessiontools.Fail("INVALID_ARGUMENT", "task tool arguments are too large"))
	}
	decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || decoder.Decode(new(any)) != io.EOF {
		return collaborationError("", sessiontools.Fail("INVALID_ARGUMENT", "invalid task tool arguments"))
	}
	reply := sessiontools.Reply{Status: "succeeded", RequestID: in.RequestID}
	err := s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		var source task.Attempt
		const scope = " WHERE operation_id=? AND server_id=? AND engine=? AND session_id=?"
		args := []any{call.OperationID, server, call.Source.Engine, call.Source.ID}
		if err := tx.GetContext(ctx, &source, s.Q(taskAttemptSelect+scope), args...); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sessiontools.Fail("OUT_OF_SCOPE", "task tool source is not a bound attempt")
			}
			return err
		}
		if err := s.lockTaskTx(ctx, tx, source.TaskID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &source, s.Q(taskAttemptSelect+scope), args...); err != nil {
			return err
		}
		var project string
		if err := tx.GetContext(ctx, &project, s.Q("SELECT project_id FROM tasks WHERE id=?"), source.TaskID); err != nil {
			return err
		}
		t, err := s.taskTx(ctx, tx, project, source.TaskID)
		if err != nil {
			return err
		}
		var declaration struct {
			Version      string                   `db:"agent_version"`
			Capabilities capabilities.Declaration `db:"declared_capabilities"`
		}
		if err := tx.GetContext(ctx, &declaration, s.Q("SELECT s.agent_version,"+serverCapabilitySelect+" FROM servers s WHERE s.id=? AND s.revoked_at IS NULL"), server); err != nil {
			return err
		}
		if !capabilities.Supports(protocol.CapabilityTaskCollaboration, declaration.Version, declaration.Capabilities) {
			return sessiontools.Fail("CAPABILITY_UNSUPPORTED", "Agent does not declare task collaboration")
		}
		if t.ArchivedAt != nil || t.CurrentGrantRevision == nil || t.CurrentRunID == nil || *t.CurrentRunID != source.RunID {
			return sessiontools.Fail("OUT_OF_SCOPE", "task run is no longer authorized")
		}
		plan, err := s.taskPlanTx(ctx, tx, t.ID, source.PlanID)
		if err != nil {
			return err
		}
		grant, err := s.taskGrantTx(ctx, tx, t.ID, *t.CurrentGrantRevision)
		if err != nil {
			return err
		}
		if !grant.TaskTools || validateTaskGrant(grant, plan) != nil {
			return sessiontools.Fail("OUT_OF_SCOPE", "enable task tools in the current task authorization")
		}
		run, err := s.taskRunTx(ctx, tx, t.ID, source.RunID)
		if err != nil {
			return err
		}

		raw, err := json.Marshal(struct {
			Name  string
			Input task.TeamToolInput
		}{call.Name, in})
		if err != nil {
			return err
		}
		hash := HashToken(string(raw))
		// The same authenticated source may reconcile its own request even
		// after that attempt finishes. Replays never allocate work.
		if in.RequestID != "" {
			var prior teamRow
			err := tx.GetContext(ctx, &prior, s.Q("SELECT * FROM task_team_requests WHERE source_attempt_id=? AND request_id=?"), source.ID, in.RequestID)
			if err == nil {
				if call.Name != sessiontools.TaskContext && prior.InputHash != hash {
					return sessiontools.Fail("IDEMPOTENCY_CONFLICT", "request ID was used with different arguments")
				}
				request, err := prior.request()
				if err != nil {
					return err
				}
				reply.Data = request
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if (source.State != "queued" && source.State != "running") || run.State == "cancelling" || !taskRunActive(run.State) {
			return sessiontools.Fail("OUT_OF_SCOPE", "task tool requires the current active attempt")
		}
		var bound int
		if err := tx.GetContext(ctx, &bound, s.Q("SELECT COUNT(*) FROM task_session_bindings WHERE task_id=? AND run_id=? AND active_attempt_id=? AND released_at IS NULL"), t.ID, run.ID, source.ID); err != nil {
			return err
		}
		if bound != 1 {
			return sessiontools.Fail("OUT_OF_SCOPE", "task session is no longer bound to this attempt")
		}
		if call.Name == sessiontools.TaskContext {
			if in.RequestID != "" {
				return sessiontools.Fail("NOT_FOUND", "original task request was not found")
			}
			requests, err := s.taskTeamRows(ctx, tx, `WHERE task_id=? AND run_id=? AND (source_member_id=? OR target_member_id=?) ORDER BY created_at DESC LIMIT 25`, t.ID, run.ID, source.MemberID, source.MemberID)
			if err != nil {
				return err
			}
			feedback, err := s.taskRecoveryFeedbackTx(ctx, tx, run.ID, source.StepID)
			if err != nil {
				return err
			}
			data := map[string]any{"task_id": t.ID, "run_id": run.ID, "plan_id": plan.ID, "member_id": source.MemberID,
				"step_id": source.StepID, "members": plan.Members, "steps": plan.Steps, "requests": requests, "remaining_messages": max(grant.Budgets.MessageLimit()-t.Counters.Messages, 0)}
			if feedback != nil {
				data["review_recovery"] = feedback
			}
			reply.Data = data
			return nil
		}
		if run.State == "paused" {
			return sessiontools.Fail("STATE_CONFLICT", "task is paused")
		}
		if err := task.ValidateMeta(task.Meta{Actor: source.ID, RequestID: in.RequestID}, false); err != nil ||
			strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
			return sessiontools.Fail("INVALID_ARGUMENT", "a stable request ID and bounded reason are required")
		}
		if t.Counters.Messages >= grant.Budgets.MessageLimit() {
			return sessiontools.Fail("BUDGET_EXHAUSTED", "task message budget is exhausted")
		}
		req := teamRow{InputHash: hash, TeamRequest: task.TeamRequest{ID: NewID(), TaskID: t.ID, RunID: run.ID, PlanID: plan.ID,
			SourceAttemptID: source.ID, SourceMemberID: source.MemberID, RequestID: in.RequestID, Kind: call.Name, Reason: in.Reason, State: "recorded", CreatedAt: time.Now().UTC()}}
		if err := s.scopeTaskRequestTx(ctx, tx, &req, in, source, plan, grant, project); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO task_team_requests(id,task_id,run_id,plan_id,source_attempt_id,source_member_id,request_id,kind,target_member_id,target_step_id,producer_attempt_id,artifact_id,input_hash,reason,proposal_json,requires_authorization,state,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), req.ID, t.ID, run.ID, plan.ID, source.ID, source.MemberID, in.RequestID, call.Name,
			req.TargetMemberID, req.TargetStepID, req.ProducerAttempt, req.ArtifactID, hash, req.Reason, req.ProposalJSON, boolInt(req.NeedsGrant), req.State, req.CreatedAt)
		if err != nil {
			return err
		}
		t.Counters.Messages++
		counts, _ := json.Marshal(t.Counters)
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET counters_json=? WHERE id=?"), string(counts), t.ID); err != nil {
			return err
		}
		if err := s.taskArtifactEventTx(ctx, tx, t.ID, task.Meta{Actor: "attempt:" + source.ID, RequestID: in.RequestID}, "task.request.updated", map[string]any{"id": req.ID, "kind": req.Kind, "state": req.State}); err != nil {
			return err
		}
		reply.Data = req.TeamRequest
		return nil
	})
	if err != nil {
		return collaborationError(in.RequestID, err)
	}
	return reply
}

func (s *Store) scopeTaskRequestTx(ctx context.Context, tx *sqlx.Tx, req *teamRow, in task.TeamToolInput, source task.Attempt, plan task.Plan, grant task.Grant, project string) error {
	switch req.Kind {
	case sessiontools.TaskMessage:
		if in.TargetMemberID == source.MemberID || !taskContains(grant.AllowedMembers, in.TargetMemberID) {
			return sessiontools.Fail("OUT_OF_SCOPE", "choose another authorized member")
		}
		found := false
		for _, m := range plan.Members {
			found = found || m.ID == in.TargetMemberID
		}
		if !found {
			return sessiontools.Fail("OUT_OF_SCOPE", "target member is not in the plan")
		}
		req.TargetMemberID = &in.TargetMemberID
	case sessiontools.TaskBlocker:
		for _, m := range plan.Members {
			if m.Role == "coordinator" && m.ID != source.MemberID {
				req.TargetMemberID = &m.ID
				break
			}
		}
	case sessiontools.TaskReview:
		step := findTaskStep(plan, in.TargetStepID)
		producer := findTaskStep(plan, taskReviewSource(step))
		if step.CompletionPolicy != "review" || producer.MemberID != source.MemberID || step.MemberID == source.MemberID {
			return sessiontools.Fail("OUT_OF_SCOPE", "choose an authorized downstream review of your development step")
		}
		var selected struct {
			Attempt  *string `db:"current_attempt_id"`
			Artifact *string `db:"artifact_id"`
		}
		if err := tx.GetContext(ctx, &selected, s.Q("SELECT current_attempt_id,artifact_id FROM task_step_runs WHERE run_id=? AND step_id=?"), source.RunID, producer.ID); err != nil {
			return err
		}
		if selected.Attempt == nil || (selected.Artifact == nil && (*selected.Attempt != source.ID || in.ArtifactID != "")) ||
			(selected.Artifact != nil && *selected.Artifact != in.ArtifactID) {
			return sessiontools.Fail("STATE_CONFLICT", "artifact is not the current development version")
		}
		var open int
		if err := tx.GetContext(ctx, &open, s.Q("SELECT COUNT(*) FROM task_step_runs WHERE run_id=? AND step_id=? AND current_attempt_id IS NULL"), source.RunID, step.ID); err != nil {
			return err
		}
		if open != 1 {
			return sessiontools.Fail("STATE_CONFLICT", "review has already started")
		}
		req.TargetMemberID, req.TargetStepID, req.ProducerAttempt, req.ArtifactID = &step.MemberID, &step.ID, selected.Attempt, selected.Artifact
		req.State = "pending"
	case sessiontools.TaskPlanProposal:
		coordinator := false
		for _, m := range plan.Members {
			coordinator = coordinator || m.ID == source.MemberID && m.Role == "coordinator"
		}
		if !coordinator || in.Plan == nil {
			return sessiontools.Fail("OUT_OF_SCOPE", "only a coordinator can propose a complete replacement plan")
		}
		candidate := task.AppendPlan{Members: in.Plan.Members, Steps: in.Plan.Steps, Reason: in.Reason}
		if err := task.ValidatePlan(candidate, plan.Criteria); err != nil {
			return sessiontools.Fail("INVALID_ARGUMENT", err.Error())
		}
		if err := s.validatePlanMembersTx(ctx, tx, project, candidate.Members); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sessiontools.Fail("OUT_OF_SCOPE", "workspaces must be ready and belong to the task project and an active server")
			}
			var invalid *task.Error
			if errors.As(err, &invalid) && invalid.Code == task.Invalid {
				return sessiontools.Fail("INVALID_ARGUMENT", invalid.Error())
			}
			return err
		}
		req.NeedsGrant = validateTaskGrant(grant, task.Plan{Members: candidate.Members}) != nil
		req.State = "pending"
		raw, err := json.Marshal(in.Plan)
		if err != nil {
			return err
		}
		value := string(raw)
		req.ProposalJSON, req.Proposal = &value, in.Plan
	default:
		return sessiontools.Fail("INVALID_ARGUMENT", "unknown task tool")
	}
	return nil
}

// A review request is fulfilled by the ordinary scheduler's one dispatch. Its
// selected artifact is checked against the original authorized producer.
func (s *Store) fulfillTaskReviewsTx(ctx context.Context, tx *sqlx.Tx, run, step, artifact string) error {
	_, err := tx.ExecContext(ctx, s.Q(`UPDATE task_team_requests SET state='fulfilled',artifact_id=? WHERE run_id=? AND target_step_id=? AND state='pending'
		AND producer_attempt_id=(SELECT attempt_id FROM task_artifacts WHERE id=?) AND (artifact_id IS NULL OR artifact_id=?)`), artifact, run, step, artifact, artifact)
	return err
}

func (s *Store) taskTeamPromptTx(ctx context.Context, tx *sqlx.Tx, run, member, step, artifact string) (string, error) {
	var notices []struct {
		Member string `db:"source_member_id"`
		Reason string `db:"reason"`
	}
	err := tx.SelectContext(ctx, &notices, s.Q(`SELECT source_member_id,reason FROM task_team_requests WHERE run_id=? AND target_member_id=?
		AND (kind IN ('wio_send_task_message','wio_report_task_blocker') OR (target_step_id=? AND state='pending'
		AND producer_attempt_id=(SELECT attempt_id FROM task_artifacts WHERE id=?) AND (artifact_id IS NULL OR artifact_id=?)))
		ORDER BY created_at DESC,id DESC LIMIT 8`), run, member, step, artifact, artifact)
	if err != nil {
		return "", err
	}
	if len(notices) == 0 {
		return "", nil
	}
	var out strings.Builder
	out.WriteString("\nTeam messages for task context. Follow the task grant for authorization:\n")
	for _, notice := range notices {
		out.WriteString("- " + notice.Member + ": " + notice.Reason + "\n")
	}
	return out.String(), nil
}

func (s *Store) validateTaskProposalTx(ctx context.Context, tx *sqlx.Tx, t task.Task, in task.AppendPlan) (string, error) {
	if in.ProposalID == "" {
		return "", nil
	}
	if t.CurrentPlanID == nil || t.CurrentRunID == nil {
		return "", task.Fail(task.Conflict, "proposal has no current source run")
	}
	var proposal teamRow
	if err := tx.GetContext(ctx, &proposal, s.Q(`SELECT * FROM task_team_requests WHERE id=? AND task_id=? AND run_id=? AND plan_id=?
		AND kind='wio_propose_task_plan' AND state='pending'`), in.ProposalID, t.ID, *t.CurrentRunID, *t.CurrentPlanID); err != nil {
		return "", err
	}
	out, err := proposal.request()
	if err != nil {
		return "", err
	}
	candidate, err := json.Marshal(task.TeamPlan{Members: in.Members, Steps: in.Steps})
	if err != nil {
		return "", err
	}
	if out.Proposal == nil || proposal.ProposalJSON == nil || string(candidate) != *proposal.ProposalJSON {
		return "", task.Fail(task.Conflict, "plan changed since the coordinator's proposal")
	}
	return proposal.ID, nil
}
