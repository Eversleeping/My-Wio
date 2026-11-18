package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/task"
	"github.com/wio-platform/wio/internal/taskartifact"
)

const taskReviewRecoveryQueued = "task.review.recovery.queued"

func findTaskStep(plan task.Plan, id string) task.Step {
	for _, step := range plan.Steps {
		if step.ID == id {
			return step
		}
	}
	return task.Step{}
}

func taskReviewSource(step task.Step) string {
	for _, in := range step.Inputs {
		if in.Kind == "artifact" {
			return in.StepID
		}
	}
	return ""
}

func (s *Store) taskReviewInputTx(ctx context.Context, tx *sqlx.Tx, run, workspace string, step task.Step) (string, *protocol.TaskArtifactHandoff, error) {
	if step.CompletionPolicy != "review" {
		return "", nil, nil
	}
	var selected struct {
		ID       string `db:"id"`
		Digest   string `db:"manifest_hash"`
		Manifest string `db:"manifest_json"`
	}
	err := tx.GetContext(ctx, &selected, s.Q(`SELECT a.id,a.manifest_hash,a.manifest_json FROM task_step_runs st
		JOIN task_artifacts a ON a.id=st.artifact_id AND a.attempt_id=st.current_attempt_id AND a.run_id=st.run_id
		WHERE st.run_id=? AND st.step_id=? AND a.state='ready'`), run, taskReviewSource(step))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var prepared struct {
		ArtifactID   string                   `db:"artifact_id"`
		State        string                   `db:"state"`
		AgentVersion string                   `db:"agent_version"`
		Declared     capabilities.Declaration `db:"declared_capabilities"`
	}
	err = tx.GetContext(ctx, &prepared, s.Q(`SELECT h.artifact_id,h.state,s.agent_version,`+serverCapabilitySelect+`
		FROM task_artifact_handoffs h JOIN workspaces w ON w.id=h.workspace_id JOIN servers s ON s.id=w.server_id
		WHERE h.workspace_id=? AND h.action='apply' AND h.state<>'failed' ORDER BY h.created_at DESC,h.id DESC LIMIT 1`), workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if prepared.ArtifactID != selected.ID || prepared.State != "ready" {
		return "", nil, nil
	}
	out := &protocol.TaskArtifactHandoff{Action: "verify", Digest: selected.Digest}
	if err := json.Unmarshal([]byte(selected.Manifest), &out.Manifest); err != nil {
		return "", nil, err
	}
	digest, err := taskartifact.Validate(out.Manifest)
	if err != nil || digest != out.Digest {
		return "", nil, task.Fail(task.Conflict, "review artifact evidence is invalid")
	}
	if capabilities.Supports(protocol.CapabilityTaskArtifactVerify, prepared.AgentVersion, prepared.Declared) {
		out.MetadataOnly = true
		for i := range out.Manifest.Files {
			out.Manifest.Files[i].Content = nil
		}
	}
	return selected.ID, out, nil
}

func (s *Store) bindTaskReviewTx(ctx context.Context, tx *sqlx.Tx, id, run, attempt, operation, artifact string, plan task.Plan, step task.Step, input *protocol.TaskArtifactHandoff) error {
	var raw string
	if err := tx.GetContext(ctx, &raw, s.Q("SELECT payload FROM agent_operations WHERE id=?"), operation); err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	payload["task_review_snapshot"] = encoded
	command, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET payload=? WHERE id=?"), string(command), operation); err != nil {
		return err
	}
	criteria := []task.Criterion{}
	for _, criterion := range plan.Criteria {
		if taskContains(step.CriterionIDs, criterion.ID) {
			criteria = append(criteria, criterion)
		}
	}
	criteriaJSON, err := json.Marshal(criteria)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO task_reviews(attempt_id,task_id,run_id,artifact_id,artifact_digest,reviewer_step_id,criteria_json)
		VALUES(?,?,?,?,?,?,?)`), attempt, id, run, artifact, input.Digest, step.ID, string(criteriaJSON))
	return err
}

type taskReviewRow struct {
	task.Review
	CriteriaJSON string  `db:"criteria_json"`
	ChecksJSON   *string `db:"checks_json"`
	Outcome      string  `db:"attempt_state"`
	Certainty    string  `db:"outcome_certainty"`
	Summary      string  `db:"summary"`
	Operation    string  `db:"operation"`
}

const taskReviewSelect = `SELECT r.*,a.state AS attempt_state,a.outcome_certainty,COALESCE(a.result_summary,'') AS summary,COALESCE(a.operation_id,'') AS operation
	FROM task_reviews r JOIN task_attempts a ON a.id=r.attempt_id`

func (row taskReviewRow) review() (task.Review, error) {
	r := row.Review
	if err := json.Unmarshal([]byte(row.CriteriaJSON), &r.Criteria); err != nil {
		return r, err
	}
	if row.ChecksJSON != nil {
		if err := json.Unmarshal([]byte(*row.ChecksJSON), &r.Checks); err != nil {
			return r, err
		}
	}
	if r.EvidenceHash == nil && row.Outcome == "succeeded" && row.Certainty == "confirmed" && row.Operation != "" && row.Summary != "" {
		evidence := HashToken(strings.Join([]string{r.AttemptID, row.Operation, r.Digest, row.CriteriaJSON, row.Summary}, "\x00"))
		r.EvidenceHash = &evidence
	}
	return r, nil
}

func (s *Store) TaskReviews(ctx context.Context, project, id string) ([]task.Review, error) {
	out := []task.Review{}
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := s.taskTx(ctx, tx, project, id); err != nil {
			return err
		}
		var rows []taskReviewRow
		if err := tx.SelectContext(ctx, &rows, s.Q(taskReviewSelect+" WHERE r.task_id=? ORDER BY a.created_at DESC,a.number DESC,a.id"), id); err != nil {
			return err
		}
		out = out[:0]
		for _, row := range rows {
			review, err := row.review()
			if err != nil {
				return err
			}
			out = append(out, review)
		}
		return nil
	})
	return out, err
}

func validateReviewChecks(r task.Review, in task.ReviewDecision) error {
	if (in.Verdict != "approved" && in.Verdict != "rejected" && in.Verdict != "inconclusive") || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
		return task.Fail(task.Invalid, "provide a review verdict and reason")
	}
	if len(r.Criteria) != len(in.Checks) {
		return task.Fail(task.Invalid, "record every required criterion")
	}
	criteria := map[string]bool{}
	for _, c := range r.Criteria {
		criteria[c.ID] = true
	}
	for _, check := range in.Checks {
		if !criteria[check.CriterionID] || len(check.Evidence) > 4096 {
			return task.Fail(task.Invalid, "invalid or duplicate review criterion")
		}
		delete(criteria, check.CriterionID)
		switch check.State {
		case "passed", "failed", "unknown", "not_run":
		default:
			return task.Fail(task.Invalid, "unknown check status")
		}
		if (check.State == "passed" || check.State == "failed") && strings.TrimSpace(check.Evidence) == "" {
			return task.Fail(task.Invalid, "record evidence for completed checks")
		}
		if in.Verdict == "approved" && check.State != "passed" {
			return task.Fail(task.StateConflict, "every required check must pass before approval")
		}
	}
	return nil
}

func (s *Store) DecideTaskReview(ctx context.Context, project, id string, in task.ReviewDecision) (task.Review, error) {
	var out task.Review
	err := s.taskCommand(ctx, project, id, "review:"+in.AttemptID, in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if t.Version != in.ExpectedVersion {
			return task.Fail(task.Conflict, "task version changed")
		}
		if t.ArchivedAt != nil || t.CurrentRunID == nil || t.State != "active" {
			return task.Fail(task.StateConflict, "review has no active run")
		}
		run, err := s.taskRunTx(ctx, tx, id, *t.CurrentRunID)
		if err != nil {
			return err
		}
		if run.State == "cancelling" {
			return task.Fail(task.StateConflict, "run is cancelling")
		}
		var row taskReviewRow
		if err := tx.GetContext(ctx, &row, s.Q(taskReviewSelect+" WHERE r.task_id=? AND r.run_id=? AND r.attempt_id=?"), id, run.ID, in.AttemptID); err != nil {
			return err
		}
		review, err := row.review()
		if err != nil {
			return err
		}
		if review.Verdict != nil || review.EvidenceHash == nil || *review.EvidenceHash != in.EvidenceHash || review.ArtifactID != in.ArtifactID {
			return task.Fail(task.Conflict, "review evidence or artifact changed")
		}
		if err := validateReviewChecks(review, in); err != nil {
			return err
		}
		plan, err := s.taskPlanTx(ctx, tx, id, run.PlanID)
		if err != nil {
			return err
		}
		var current string
		if err := tx.GetContext(ctx, &current, s.Q("SELECT COALESCE(artifact_id,'') FROM task_step_runs WHERE run_id=? AND step_id=?"), run.ID, taskReviewSource(findTaskStep(plan, review.StepID))); err != nil {
			return err
		}
		if current != review.ArtifactID {
			return task.Fail(task.Conflict, "review refers to a superseded artifact")
		}
		newState, code := "blocked", any("review_"+in.Verdict)
		if in.Verdict == "approved" {
			newState, code = "succeeded", nil
		}
		result, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state=?,block_code=?,version=version+1 WHERE run_id=? AND step_id=? AND current_attempt_id=? AND block_code='review_required'"), newState, code, run.ID, review.StepID, in.AttemptID)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return task.Fail(task.StateConflict, "review is not awaiting a decision")
		}
		now := time.Now().UTC()
		checks, err := json.Marshal(in.Checks)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_reviews SET verdict=?,reason=?,checks_json=?,evidence_hash=?,decided_at=?,decided_by=? WHERE attempt_id=?"), in.Verdict, in.Reason, string(checks), in.EvidenceHash, now, in.Actor, in.AttemptID); err != nil {
			return err
		}
		out = review
		out.Verdict, out.Reason, out.DecidedBy, out.DecidedAt, out.Checks = &in.Verdict, &in.Reason, &in.Actor, &now, in.Checks
		return s.taskArtifactEventTx(ctx, tx, id, in.Meta, "task.review.recorded", map[string]any{"run_id": run.ID, "attempt_id": review.AttemptID, "artifact_id": review.ArtifactID, "verdict": in.Verdict, "evidence_hash": in.EvidenceHash})
	})
	return out, err
}

func (s *Store) taskStepDefinitionTx(ctx context.Context, tx *sqlx.Tx, a task.Attempt) (task.Step, error) {
	var raw string
	var step task.Step
	if err := tx.GetContext(ctx, &raw, s.Q("SELECT definition_json FROM task_plan_steps WHERE plan_id=? AND id=?"), a.PlanID, a.StepID); err != nil {
		return step, err
	}
	err := json.Unmarshal([]byte(raw), &step)
	return step, err
}

func (s *Store) taskReworkTx(ctx context.Context, tx *sqlx.Tx, t task.Task, r task.Run, in task.Action) error {
	if r.State != "running" && r.State != "blocked" && r.State != "paused" {
		return task.Fail(task.StateConflict, "run does not allow review recovery")
	}
	if t.CurrentGrantRevision == nil {
		return task.Fail(task.Forbidden, "execution authorization is required")
	}
	plan, err := s.taskPlanTx(ctx, tx, t.ID, r.PlanID)
	if err != nil {
		return err
	}
	grant, err := s.taskGrantTx(ctx, tx, t.ID, *t.CurrentGrantRevision)
	if err != nil {
		return err
	}
	if err := validateTaskGrant(grant, plan); err != nil {
		return err
	}
	var row taskReviewRow
	if err := tx.GetContext(ctx, &row, s.Q(taskReviewSelect+" WHERE r.task_id=? AND r.run_id=? AND r.attempt_id=?"), t.ID, r.ID, in.AttemptID); err != nil {
		return err
	}
	if row.Verdict == nil {
		return task.Fail(task.StateConflict, "review decision is required")
	}
	root := row.StepID
	if in.Action == "rework" {
		if *row.Verdict != "rejected" {
			return task.Fail(task.StateConflict, "only a rejected review allows rework")
		}
		root = taskReviewSource(findTaskStep(plan, root))
	} else if *row.Verdict != "inconclusive" {
		return task.Fail(task.StateConflict, "only an inconclusive review can be repeated")
	}
	var current string
	if err := tx.GetContext(ctx, &current, s.Q("SELECT COALESCE(current_attempt_id,'') FROM task_step_runs WHERE run_id=? AND step_id=?"), r.ID, row.StepID); err != nil {
		return err
	}
	if current != row.AttemptID {
		return task.Fail(task.StateConflict, "review has already been replaced")
	}
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT reworks FROM task_step_runs WHERE run_id=? AND step_id=?"), r.ID, root); err != nil {
		return err
	}
	if count >= grant.Budgets.ReworkLimit() {
		return task.Fail(task.StateConflict, "review recovery budget is exhausted")
	}
	affected := taskDescendants(plan, root)
	if t.Counters.Attempts+len(affected) > grant.Budgets.Attempts {
		return task.Fail(task.StateConflict, "not enough task attempt budget for review recovery")
	}
	type invalidated struct {
		Step     string  `json:"step_id" db:"step_id"`
		Attempt  *string `json:"attempt_id" db:"current_attempt_id"`
		Artifact *string `json:"artifact_id" db:"artifact_id"`
	}
	var history []invalidated
	review, err := row.review()
	if err != nil {
		return err
	}
	feedback := taskFeedbackFromReview(review, in.Reason)
	rawFeedback, err := json.Marshal(feedback)
	if err != nil {
		return err
	}
	for _, step := range affected {
		var active, attempts int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND step_id=? AND state IN ('queued','running','unknown')"), r.ID, step); err != nil {
			return err
		}
		if active > 0 {
			return task.Fail(task.StateConflict, "stop affected steps before review recovery")
		}
		if err := tx.GetContext(ctx, &attempts, s.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND step_id=?"), r.ID, step); err != nil {
			return err
		}
		if attempts >= grant.Budgets.StepAttempts {
			return task.Fail(task.StateConflict, fmt.Sprintf("attempt budget exhausted for step %s", step))
		}
		var prior invalidated
		if err := tx.GetContext(ctx, &prior, s.Q("SELECT step_id,current_attempt_id,artifact_id FROM task_step_runs WHERE run_id=? AND step_id=?"), r.ID, step); err != nil {
			return err
		}
		history = append(history, prior)
		state, charge := "pending", 0
		var recovery *string
		if step == root {
			state, charge = "ready", 1
			value := string(rawFeedback)
			recovery = &value
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state=?,current_attempt_id=NULL,artifact_id=NULL,block_code=NULL,reworks=reworks+?,recovery_json=?,version=version+1 WHERE run_id=? AND step_id=?"), state, charge, recovery, r.ID, step); err != nil {
			return err
		}
	}
	return s.taskEventTx(ctx, tx, t.ID, taskReviewRecoveryQueued, in.Meta, map[string]any{"run_id": r.ID, "root_step_id": root, "review_attempt_id": row.AttemptID, "invalidated": history})
}

func taskDescendants(plan task.Plan, root string) []string {
	ids := []string{root}
	seen := map[string]bool{root: true}
	for cursor := 0; cursor < len(ids); cursor++ {
		for _, step := range plan.Steps {
			if !seen[step.ID] && taskContains(step.DependsOn, ids[cursor]) {
				seen[step.ID] = true
				ids = append(ids, step.ID)
			}
		}
	}
	return ids
}

type taskRecoveryFeedback struct {
	ReviewAttempt string             `json:"review_attempt_id"`
	Verdict       string             `json:"verdict"`
	ReviewReason  *string            `json:"review_reason,omitempty"`
	Reason        string             `json:"recovery_reason,omitempty"`
	Checks        []task.ReviewCheck `json:"checks,omitempty"`
}

func taskFeedbackFromReview(review task.Review, reason string) taskRecoveryFeedback {
	feedback := taskRecoveryFeedback{ReviewAttempt: review.AttemptID, Verdict: *review.Verdict, ReviewReason: review.Reason, Reason: reason}
	for _, check := range review.Checks {
		if check.State != "passed" {
			feedback.Checks = append(feedback.Checks, check)
		}
	}
	return feedback
}

func (s *Store) taskRecoveryFeedbackTx(ctx context.Context, tx *sqlx.Tx, run, step string) (*taskRecoveryFeedback, error) {
	var row struct {
		Task     string  `db:"task_id"`
		Reworks  int     `db:"reworks"`
		Recovery *string `db:"recovery_json"`
	}
	if err := tx.GetContext(ctx, &row, s.Q("SELECT task_id,reworks,recovery_json FROM task_step_runs WHERE run_id=? AND step_id=?"), run, step); err != nil {
		return nil, err
	}
	if row.Recovery == nil {
		if row.Reworks == 0 {
			return nil, nil
		}
		return s.legacyTaskRecoveryFeedbackTx(ctx, tx, row.Task, run, step)
	}
	var feedback taskRecoveryFeedback
	if err := json.Unmarshal([]byte(*row.Recovery), &feedback); err != nil {
		return nil, err
	}
	return &feedback, nil
}

// Older recovery events retained the review and the invalidation set.
func (s *Store) legacyTaskRecoveryFeedbackTx(ctx context.Context, tx *sqlx.Tx, id, run, step string) (*taskRecoveryFeedback, error) {
	var events []string
	if err := tx.SelectContext(ctx, &events, s.Q("SELECT payload FROM events WHERE stream_id=? AND kind=? ORDER BY sequence DESC"), "task:"+id, taskReviewRecoveryQueued); err != nil {
		return nil, err
	}
	for _, raw := range events {
		var event task.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		var recovery struct {
			Run         string `json:"run_id"`
			Root        string `json:"root_step_id"`
			Review      string `json:"review_attempt_id"`
			Invalidated []struct {
				Step string `json:"step_id"`
			} `json:"invalidated"`
		}
		if err := json.Unmarshal(event.Payload, &recovery); err != nil {
			return nil, err
		}
		if recovery.Run != run {
			continue
		}
		if recovery.Root == step {
			var row taskReviewRow
			if err := tx.GetContext(ctx, &row, s.Q(taskReviewSelect+" WHERE r.task_id=? AND r.run_id=? AND r.attempt_id=?"), id, run, recovery.Review); err != nil {
				return nil, err
			}
			review, err := row.review()
			if err != nil || review.Verdict == nil {
				return nil, err
			}
			feedback := taskFeedbackFromReview(review, "")
			return &feedback, nil
		}
		for _, invalidated := range recovery.Invalidated {
			if invalidated.Step == step {
				return nil, nil
			}
		}
	}
	return nil, nil
}
