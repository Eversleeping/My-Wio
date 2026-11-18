package store

import (
	"context"
	"github.com/jmoiron/sqlx"
)

func migrateTaskTeam(ctx context.Context, tx *sqlx.Tx, _ string) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE task_team_requests (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL, run_id TEXT NOT NULL, plan_id TEXT NOT NULL,
		source_attempt_id TEXT NOT NULL, source_member_id TEXT NOT NULL, request_id TEXT NOT NULL,
		kind TEXT NOT NULL, target_member_id TEXT, target_step_id TEXT,
		producer_attempt_id TEXT, artifact_id TEXT,
		input_hash TEXT NOT NULL, reason TEXT NOT NULL, proposal_json TEXT,
		requires_authorization INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL CHECK(state IN ('recorded','pending','fulfilled','adopted','superseded')),
		created_at TIMESTAMP NOT NULL,
		UNIQUE(source_attempt_id,request_id),
		FOREIGN KEY(task_id,run_id,source_attempt_id) REFERENCES task_attempts(task_id,run_id,id) ON DELETE RESTRICT,
		FOREIGN KEY(task_id,run_id,plan_id) REFERENCES task_runs(task_id,id,plan_id) ON DELETE RESTRICT
	)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE INDEX task_team_requests_run_idx ON task_team_requests(task_id,run_id,created_at)`)
	return err
}
