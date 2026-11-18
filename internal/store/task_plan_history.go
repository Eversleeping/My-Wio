package store

import (
	"context"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/task"
)

func (s *Store) TaskPlans(ctx context.Context, project, id string) ([]task.PlanVersion, error) {
	out := []task.PlanVersion{}
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		out = out[:0]
		if _, err := s.taskTx(ctx, tx, project, id); err != nil {
			return err
		}
		return tx.SelectContext(ctx, &out, s.Q("SELECT id,revision,parent_id,created_at FROM task_plan_revisions WHERE task_id=? ORDER BY revision DESC"), id)
	})
	return out, err
}

func (s *Store) TaskPlan(ctx context.Context, project, id, plan string) (task.Plan, error) {
	var out task.Plan
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := s.taskTx(ctx, tx, project, id); err != nil {
			return err
		}
		var err error
		out, err = s.taskPlanTx(ctx, tx, id, plan)
		return err
	})
	return out, err
}
