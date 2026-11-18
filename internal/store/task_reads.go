package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/task"
)

func (s *Store) taskReadTx(ctx context.Context, read func(*sqlx.Tx) error) error {
	options := &sql.TxOptions{ReadOnly: true}
	if s.isPostgres() {
		options.Isolation = sql.LevelRepeatableRead
	}
	return s.inTx(ctx, options, read)
}
func (s *Store) TaskDetail(ctx context.Context, project, id string) (task.Detail, error) {
	var result task.Detail
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		result = task.Detail{Steps: []task.StepRun{}, Members: []task.Member{}, Attempts: []task.Attempt{}, Blockers: []string{}, Capabilities: map[string]bool{"edit_plan": true, "start_run": false, "artifacts": true, "reviews": true, "collaboration": true, "graph": true}}
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		result.Task = t
		result.Capabilities["edit_plan"] = t.ArchivedAt == nil && t.State != "active" && t.State != "awaiting_acceptance"
		if t.CurrentPlanID != nil {
			p, err := s.taskPlanTx(ctx, tx, id, *t.CurrentPlanID)
			if err != nil {
				return err
			}
			result.Plan = &p
			result.Members = p.Members
		}
		if t.CurrentGrantRevision != nil {
			g, err := s.taskGrantTx(ctx, tx, id, *t.CurrentGrantRevision)
			if err != nil {
				return err
			}
			result.Grant = &g
		}
		result.Capabilities["start_run"] = result.Capabilities["edit_plan"] && result.Plan != nil && result.Grant != nil && result.Plan.SpecVersion == t.SpecVersion && t.Counters.Runs < result.Grant.Budgets.Runs && t.Counters.Attempts < result.Grant.Budgets.Attempts && validateTaskGrant(*result.Grant, *result.Plan) == nil
		if t.CurrentRunID != nil {
			run, err := s.taskRunTx(ctx, tx, id, *t.CurrentRunID)
			if err != nil {
				return err
			}
			result.Run = &run
			if result.Plan == nil || result.Plan.ID != run.PlanID {
				plan, err := s.taskPlanTx(ctx, tx, id, run.PlanID)
				if err != nil {
					return err
				}
				result.RunPlan = &plan
			}
			if run.State == "awaiting_acceptance" {
				result.AcceptanceEvidenceHash, err = s.taskAcceptanceHash(ctx, tx, run)
				if err != nil {
					return err
				}
			}
			var steps []struct {
				StepID   string  `db:"step_id"`
				State    string  `db:"state"`
				Version  int64   `db:"version"`
				Attempt  *string `db:"current_attempt_id"`
				Block    *string `db:"block_code"`
				Artifact *string `db:"artifact_id"`
				Reworks  int     `db:"reworks"`
			}
			if err := tx.SelectContext(ctx, &steps, s.Q("SELECT step_id,state,version,current_attempt_id,block_code,artifact_id,reworks FROM task_step_runs WHERE task_id=? AND run_id=? ORDER BY step_id"), id, run.ID); err != nil {
				return err
			}
			for _, step := range steps {
				result.Steps = append(result.Steps, task.StepRun{StepID: step.StepID, State: step.State, Version: step.Version, CurrentAttemptID: step.Attempt, BlockCode: step.Block, ArtifactID: step.Artifact, Reworks: step.Reworks})
				if step.Block != nil {
					result.Blockers = append(result.Blockers, *step.Block)
				}
			}
			if err := tx.SelectContext(ctx, &result.Attempts, s.Q(taskAttemptSelect+" WHERE task_id=? AND run_id=? ORDER BY created_at,id"), id, run.ID); err != nil {
				return err
			}
		}
		return tx.GetContext(ctx, &result.LastSequence, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), "task:"+id)
	})
	return result, err
}

type taskCursor struct {
	Project string    `json:"project"`
	State   string    `json:"state"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
}

func (s *Store) TaskPage(ctx context.Context, f task.Filter) (task.Page, error) {
	result := task.Page{Items: []task.Task{}}
	if f.ProjectID == "" || f.Limit < 1 || f.Limit > 100 {
		return result, task.Fail(task.Invalid, "project_id and a limit from 1 to 100 are required")
	}
	switch f.State {
	case "", "draft", "active", "awaiting_acceptance", "accepted", "rejected", "failed", "cancelled":
	default:
		return result, task.Fail(task.Invalid, "invalid task state")
	}
	query := "SELECT t.*," + taskStateSQL + " AS task_state FROM tasks t" + taskCurrentRunJoin + " WHERE t.project_id=?"
	args := []any{f.ProjectID}
	if f.State != "" {
		query += " AND " + taskStateSQL + "=?"
		args = append(args, f.State)
	}
	if f.Cursor != "" {
		var c taskCursor
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if len(f.Cursor) > 2048 || err != nil || json.Unmarshal(raw, &c) != nil || c.Project != f.ProjectID || c.State != f.State || c.ID == "" || c.At.IsZero() {
			return result, task.Fail(task.Invalid, "invalid task cursor for this query")
		}
		query += " AND (t.updated_at<? OR (t.updated_at=? AND t.id<?))"
		args = append(args, c.At, c.At, c.ID)
	}
	query += " ORDER BY t.updated_at DESC,t.id DESC LIMIT ?"
	args = append(args, f.Limit+1)
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		result = task.Page{Items: []task.Task{}}
		var exists string
		if err := tx.GetContext(ctx, &exists, s.Q("SELECT id FROM projects WHERE id=?"), f.ProjectID); err != nil {
			return err
		}
		rows := []taskStateRow{}
		if err := tx.SelectContext(ctx, &rows, s.Q(query), args...); err != nil {
			return err
		}
		more := len(rows) > f.Limit
		if more {
			rows = rows[:f.Limit]
		}
		result.Items = []task.Task{}
		for _, row := range rows {
			t, err := row.decode()
			if err != nil {
				return err
			}
			result.Items = append(result.Items, t)
		}
		if more {
			last := rows[len(rows)-1]
			raw, err := json.Marshal(taskCursor{Project: f.ProjectID, State: f.State, At: last.UpdatedAt, ID: last.ID})
			if err != nil {
				return err
			}
			c := base64.RawURLEncoding.EncodeToString(raw)
			result.NextCursor = &c
		}
		return nil
	})
	return result, err
}
func (s *Store) TaskEvents(ctx context.Context, project, id string, after int64, limit int) (task.EventPage, error) {
	page := task.EventPage{Items: []task.Event{}, NextAfter: after}
	if after < 0 || limit < 1 || limit > 500 {
		return page, task.Fail(task.Invalid, "invalid task event cursor or limit")
	}
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		page = task.EventPage{Items: []task.Event{}, NextAfter: after}
		if _, err := s.taskTx(ctx, tx, project, id); err != nil {
			return err
		}
		var rows []struct {
			Payload string `db:"payload"`
		}
		if err := tx.SelectContext(ctx, &rows, s.Q("SELECT payload FROM events WHERE stream_id=? AND sequence>? ORDER BY sequence LIMIT ?"), "task:"+id, after, limit+1); err != nil {
			return err
		}
		page.HasMore = len(rows) > limit
		if page.HasMore {
			rows = rows[:limit]
		}
		page.Items = []task.Event{}
		for _, row := range rows {
			var event task.Event
			if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
				return err
			}
			page.Items = append(page.Items, event)
			page.NextAfter = event.Sequence
		}
		return nil
	})
	return page, err
}
func (s *Store) TaskRequest(ctx context.Context, actor, project, request string) (task.Request, error) {
	var row struct {
		Action   string `db:"action"`
		TaskID   string `db:"task_id"`
		Response string `db:"response_json"`
		Status   int    `db:"response_status"`
	}
	err := s.DB.GetContext(ctx, &row, s.Q("SELECT action,task_id,response_json,response_status FROM task_requests WHERE actor_key=? AND project_id=? AND request_id=?"), actor, project, request)
	return task.Request{Action: row.Action, TaskID: row.TaskID, Response: json.RawMessage(row.Response), Status: row.Status}, err
}

func (s *Store) TaskProject(ctx context.Context, id string) (string, error) {
	var project string
	err := s.DB.GetContext(ctx, &project, s.Q("SELECT project_id FROM tasks WHERE id=?"), id)
	return project, err
}

func (s *Store) RecentTasks(ctx context.Context) ([]task.Recent, error) {
	rows := []task.Recent{}
	err := s.DB.SelectContext(ctx, &rows, `SELECT t.id,t.project_id,p.name AS project_name,t.title,`+taskStateSQL+` AS state
 FROM tasks t JOIN projects p ON p.id=t.project_id`+taskCurrentRunJoin+`
 WHERE t.archived_at IS NULL ORDER BY t.updated_at DESC,t.id DESC LIMIT 5`)
	return rows, err
}
