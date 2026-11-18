package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/task"
)

// The digest covers the frozen plan and the exact successful attempts the
// user reviews. Both the read model and the acceptance command compute it
// from a consistent database snapshot.
func (s *Store) taskAcceptanceHash(ctx context.Context, tx *sqlx.Tx, r task.Run) (string, error) {
	var planHash string
	if err := tx.GetContext(ctx, &planHash, s.Q("SELECT content_hash FROM task_plan_revisions WHERE task_id=? AND id=?"), r.TaskID, r.PlanID); err != nil {
		return "", err
	}
	type evidence struct {
		Step     string `db:"step_id"`
		Attempt  string `db:"id"`
		Input    string `db:"input_hash"`
		Result   string `db:"summary"`
		Cursor   int64  `db:"evidence_cursor"`
		Artifact string `db:"artifact_digest"`
		Review   string `db:"review_evidence"`
		Checks   string `db:"review_checks"`
	}
	var rows []evidence
	var required int
	if err := tx.GetContext(ctx, &required, s.Q("SELECT COUNT(*) FROM task_plan_steps WHERE plan_id=?"), r.PlanID); err != nil {
		return "", err
	}
	if err := tx.SelectContext(ctx, &rows, s.Q(`SELECT st.step_id,a.id,a.input_hash,COALESCE(a.result_summary,'') AS summary,a.evidence_cursor,
          COALESCE(art.manifest_hash,rv.artifact_digest,'') AS artifact_digest,COALESCE(rv.evidence_hash,'') AS review_evidence,COALESCE(rv.checks_json,'') AS review_checks
        FROM task_step_runs st JOIN task_attempts a ON a.id=st.current_attempt_id AND a.run_id=st.run_id AND a.step_id=st.step_id
		LEFT JOIN task_artifacts art ON art.id=st.artifact_id AND art.state='ready'
        LEFT JOIN task_reviews rv ON rv.attempt_id=a.id
        WHERE st.run_id=? AND st.state='succeeded' AND (rv.attempt_id IS NULL OR rv.verdict='approved') AND a.state='succeeded' AND a.outcome_certainty='confirmed' ORDER BY st.step_id`), r.ID); err != nil {
		return "", err
	}
	if required == 0 || len(rows) != required {
		return "", nil
	}
	raw, err := json.Marshal(struct {
		Run, Plan string
		Steps     []evidence
	}{r.ID, planHash, rows})
	if err != nil {
		return "", err
	}
	return HashToken(string(raw)), nil
}

func (s *Store) AcceptTaskRun(ctx context.Context, project, id, run string, in task.Acceptance) (task.Run, error) {
	var out task.Run
	err := s.taskCommand(ctx, project, id, "acceptance:"+run, in.Meta, in, &out, func(tx *sqlx.Tx) error {
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
		if t.CurrentRunID == nil || *t.CurrentRunID != run || t.ArchivedAt != nil || r.State != "awaiting_acceptance" {
			return task.Fail(task.StateConflict, "run is not awaiting acceptance")
		}
		if (in.Decision != "accepted" && in.Decision != "rejected") || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4096 {
			return task.Fail(task.Invalid, "provide an acceptance decision and a reason of at most 4 KiB")
		}
		var active int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM task_attempts WHERE run_id=? AND state IN ('queued','running','unknown')"), run); err != nil {
			return err
		}
		hash, err := s.taskAcceptanceHash(ctx, tx, r)
		if err != nil {
			return err
		}
		if active > 0 || hash == "" {
			return task.Fail(task.StateConflict, "run still has unresolved execution evidence")
		}
		if hash != in.EvidenceHash {
			return task.Fail(task.Conflict, "run evidence changed; refresh before deciding")
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_runs SET state=?,version=version+1,ended_at=?,accepted_at=?,accepted_by=?,acceptance_reason=? WHERE id=?"), in.Decision, now, now, in.Actor, in.Reason, run); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET released_at=?,active_attempt_id=NULL WHERE run_id=? AND released_at IS NULL"), now, run); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET version=version+1,updated_at=? WHERE id=?"), now, id); err != nil {
			return err
		}
		out, err = s.taskRunTx(ctx, tx, id, run)
		if err != nil {
			return err
		}
		return s.taskEventTx(ctx, tx, id, "task.acceptance.recorded", in.Meta, map[string]any{"run_id": run, "plan_id": r.PlanID, "decision": in.Decision, "reason": in.Reason, "evidence_hash": hash})
	})
	return out, err
}

func (s *Store) ArchiveTask(ctx context.Context, project, id string, in task.Archive) (task.Task, error) {
	var out task.Task
	err := s.taskCommand(ctx, project, id, "archive", in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if t.Version != in.ExpectedVersion {
			return task.Fail(task.Conflict, "task version changed")
		}
		if t.State == "active" || t.State == "awaiting_acceptance" {
			return task.Fail(task.StateConflict, "finish the active run before archiving")
		}
		if in.Archived == nil {
			return task.Fail(task.Invalid, "archived must be supplied")
		}
		if *in.Archived == (t.ArchivedAt != nil) {
			out = t
			return nil
		}
		now := time.Now().UTC()
		var archivedAt *time.Time
		if *in.Archived {
			archivedAt = &now
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET archived_at=?,version=version+1,updated_at=? WHERE id=?"), archivedAt, now, id); err != nil {
			return err
		}
		out, err = s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		return s.taskEventTx(ctx, tx, id, "task.archived", in.Meta, map[string]any{"archived": *in.Archived, "version": out.Version})
	})
	return out, err
}
