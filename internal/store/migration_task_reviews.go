package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

func migrateTaskReviews(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, statement := range []string{
		`ALTER TABLE task_step_runs ADD COLUMN artifact_id TEXT REFERENCES task_artifacts(id) ON DELETE RESTRICT`,
		`ALTER TABLE task_step_runs ADD COLUMN reworks INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE task_reviews (
		 attempt_id TEXT PRIMARY KEY,
		 task_id TEXT NOT NULL,
		 run_id TEXT NOT NULL,
		 artifact_id TEXT NOT NULL,
		 artifact_digest TEXT NOT NULL,
		 reviewer_step_id TEXT NOT NULL,
		 criteria_json TEXT NOT NULL,
		 verdict TEXT CHECK(verdict IN ('approved','rejected','inconclusive')),
		 checks_json TEXT,
		 reason TEXT,
		 decided_by TEXT,
		 decided_at TIMESTAMP,
		 evidence_hash TEXT,
		 FOREIGN KEY(task_id,run_id,attempt_id) REFERENCES task_attempts(task_id,run_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(task_id,artifact_id) REFERENCES task_artifacts(task_id,id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX task_reviews_run_idx ON task_reviews(task_id,run_id,attempt_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
