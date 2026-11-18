package store

import (
	"context"
	"github.com/jmoiron/sqlx"
)

func migrateTaskExecution(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, q := range []string{
		`ALTER TABLE task_attempts ADD COLUMN member_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_attempts ADD COLUMN deadline_at TIMESTAMP`,
		`ALTER TABLE task_session_bindings ADD COLUMN member_id TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX task_binding_member_active_idx ON task_session_bindings(run_id,member_id) WHERE released_at IS NULL AND member_id<>''`,
		`CREATE INDEX task_attempts_lease_idx ON task_attempts(state,lease_expires_at)`,
		`CREATE TABLE task_attempt_evidence (
		 attempt_id TEXT NOT NULL REFERENCES task_attempts(id) ON DELETE RESTRICT,
		 source_event_id TEXT NOT NULL, classification TEXT NOT NULL,
		 payload_json TEXT NOT NULL, content_hash TEXT NOT NULL, acquired_at TIMESTAMP NOT NULL,
		 PRIMARY KEY(attempt_id,source_event_id))`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
