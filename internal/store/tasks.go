package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/task"
)

type taskRow struct {
	ID                   string     `db:"id"`
	ProjectID            string     `db:"project_id"`
	Title                string     `db:"title"`
	Requirement          string     `db:"requirement"`
	Criteria             string     `db:"criteria_json"`
	SpecVersion          int64      `db:"spec_version"`
	CurrentPlanID        *string    `db:"current_plan_id"`
	CurrentRunID         *string    `db:"current_run_id"`
	CurrentGrantRevision *int64     `db:"current_grant_revision"`
	Counters             string     `db:"counters_json"`
	Version              int64      `db:"version"`
	CreatedBy            string     `db:"created_by"`
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`
	ArchivedAt           *time.Time `db:"archived_at"`
}

func (r taskRow) decode() (task.Task, error) {
	t := task.Task{ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Requirement: r.Requirement, SpecVersion: r.SpecVersion, CurrentPlanID: r.CurrentPlanID, CurrentRunID: r.CurrentRunID, CurrentGrantRevision: r.CurrentGrantRevision, Version: r.Version, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt, State: "draft"}
	if err := json.Unmarshal([]byte(r.Criteria), &t.Criteria); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(r.Counters), &t.Counters); err != nil {
		return t, err
	}
	return t, nil
}
func (s *Store) taskTx(ctx context.Context, tx *sqlx.Tx, project, id string) (task.Task, error) {
	var row taskRow
	if err := tx.GetContext(ctx, &row, s.Q("SELECT * FROM tasks WHERE id=? AND project_id=?"), id, project); err != nil {
		return task.Task{}, err
	}
	t, err := row.decode()
	if err != nil {
		return t, err
	}
	if t.CurrentRunID != nil {
		var state string
		if err := tx.GetContext(ctx, &state, s.Q("SELECT state FROM task_runs WHERE id=? AND task_id=?"), *t.CurrentRunID, t.ID); err != nil {
			return t, err
		}
		switch state {
		case "running", "paused", "blocked", "cancelling":
			t.State = "active"
		default:
			t.State = state
		}
	}
	return t, nil
}

// User commands lock the project before checking the request ledger. That makes
// simultaneous identical creates replay rather than race on a uniqueness error.
// Background dispatch uses its separate server/task lock protocol (P7-03).
func (s *Store) taskCommand(ctx context.Context, project, id, action string, meta task.Meta, input, output any, apply func(*sqlx.Tx) error) error {
	if err := task.ValidateMeta(meta, id != ""); err != nil {
		return err
	}
	if project == "" {
		return task.Fail(task.Invalid, "project_id is required")
	}
	raw, err := json.Marshal(struct {
		Action, Target string
		Input          any
	}{action, id, input})
	if err != nil {
		return err
	}
	hash := HashToken(string(raw))
	return s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		locked, err := tx.ExecContext(ctx, s.Q("UPDATE projects SET name=name WHERE id=?"), project)
		if err != nil {
			return err
		}
		n, err := locked.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		if id != "" {
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET version=version WHERE id=? AND project_id=?"), id, project); err != nil {
				return err
			}
		}
		var prior struct {
			Hash     string `db:"input_hash"`
			Response string `db:"response_json"`
		}
		err = tx.GetContext(ctx, &prior, s.Q("SELECT input_hash,response_json FROM task_requests WHERE actor_key=? AND project_id=? AND request_id=?"), meta.Actor, project, meta.RequestID)
		if err == nil {
			if prior.Hash != hash {
				return task.Fail(task.IdempotencyConflict, "request_id already has different content")
			}
			return json.Unmarshal([]byte(prior.Response), output)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := apply(tx); err != nil {
			return err
		}
		response, err := json.Marshal(output)
		if err != nil {
			return err
		}
		target := id
		status := 200
		switch result := output.(type) {
		case *task.Task:
			target = result.ID
			if action == "create" {
				status = 201
			}
		case *task.PlanResult:
			target = result.Plan.TaskID
			status = 201
		case *task.GrantResult:
			target = result.Grant.TaskID
		case *task.Review:
			target = result.TaskID
		case *task.Artifact:
			target = result.TaskID
			status = 202
		case *task.ArtifactHandoff:
			target = result.TaskID
			status = 202
		case *task.Run:
			target = result.TaskID
		case *task.RunResult:
			target = result.Run.TaskID
			if action == "start" {
				status = 201
			} else {
				status = 202
			}
		}
		_, err = tx.ExecContext(ctx, s.Q("INSERT INTO task_requests(actor_key,project_id,request_id,action,input_hash,task_id,response_json,response_status,created_at) VALUES(?,?,?,?,?,?,?,?,?)"), meta.Actor, project, meta.RequestID, action, hash, target, string(response), status, time.Now().UTC())
		return err
	})
}
func (s *Store) taskEventTx(ctx context.Context, tx *sqlx.Tx, id, kind string, meta task.Meta, payload any) error {
	sequence, err := s.nextEventSequence(ctx, tx, "task:"+id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e := task.Event{EventID: NewID(), TaskID: id, Sequence: sequence, Kind: kind, SchemaVersion: 1, Actor: meta.Actor, RequestID: meta.RequestID, Payload: raw, OccurredAt: time.Now().UTC()}
	encoded, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), e.EventID, "task:"+id, sequence, kind, e.OccurredAt, string(encoded))
	return err
}
func (s *Store) CreateTask(ctx context.Context, in task.Create) (task.Task, error) {
	var result task.Task
	err := s.taskCommand(ctx, in.ProjectID, "", "create", in.Meta, in, &result, func(tx *sqlx.Tx) error {
		if err := task.ValidateSpec(in.Title, in.Requirement, in.Criteria); err != nil {
			return err
		}
		criteria, err := json.Marshal(in.Criteria)
		if err != nil {
			return err
		}
		id := NewID()
		now := time.Now().UTC()
		_, err = tx.ExecContext(ctx, s.Q("INSERT INTO tasks(id,project_id,title,requirement,criteria_json,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)"), id, in.ProjectID, in.Title, in.Requirement, string(criteria), in.Actor, now, now)
		if err != nil {
			return err
		}
		result, err = s.taskTx(ctx, tx, in.ProjectID, id)
		if err != nil {
			return err
		}
		return s.taskEventTx(ctx, tx, id, "task.created", in.Meta, map[string]any{"version": result.Version, "spec_version": result.SpecVersion})
	})
	return result, err
}
func (s *Store) editableTaskTx(ctx context.Context, tx *sqlx.Tx, project, id string, version int64) (task.Task, error) {
	t, err := s.taskTx(ctx, tx, project, id)
	if err != nil {
		return t, err
	}
	if t.Version != version {
		return t, task.Fail(task.Conflict, "task version changed")
	}
	if t.ArchivedAt != nil {
		return t, task.Fail(task.StateConflict, "restore the task before editing")
	}
	var count int
	err = tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM task_runs WHERE task_id=? AND state IN ('running','paused','blocked','cancelling','awaiting_acceptance')"), id)
	if err == nil && count > 0 {
		err = task.Fail(task.StateConflict, "task has an active run")
	}
	return t, err
}
func (s *Store) UpdateTask(ctx context.Context, project, id string, in task.Update) (task.Task, error) {
	var result task.Task
	err := s.taskCommand(ctx, project, id, "update", in.Meta, in, &result, func(tx *sqlx.Tx) error {
		t, err := s.editableTaskTx(ctx, tx, project, id, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if in.Title == nil && in.Requirement == nil && in.Criteria == nil {
			return task.Fail(task.Invalid, "provide at least one task field")
		}
		if in.Title != nil {
			t.Title = *in.Title
		}
		if in.Requirement != nil {
			t.Requirement = *in.Requirement
		}
		if in.Criteria != nil {
			t.Criteria = *in.Criteria
		}
		if err := task.ValidateSpec(t.Title, t.Requirement, t.Criteria); err != nil {
			return err
		}
		if in.Requirement != nil || in.Criteria != nil {
			t.SpecVersion++
			t.CurrentRunID = nil
		}
		criteria, err := json.Marshal(t.Criteria)
		if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET title=?,requirement=?,criteria_json=?,spec_version=?,current_run_id=?,version=version+1,updated_at=? WHERE id=? AND project_id=? AND version=?"), t.Title, t.Requirement, string(criteria), t.SpecVersion, t.CurrentRunID, time.Now().UTC(), id, project, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := taskChanged(updated); err != nil {
			return err
		}
		result, err = s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		return s.taskEventTx(ctx, tx, id, "task.spec.updated", in.Meta, map[string]any{"version": result.Version, "spec_version": result.SpecVersion})
	})
	return result, err
}
func taskChanged(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return task.Fail(task.Conflict, "task version changed")
	}
	return nil
}
