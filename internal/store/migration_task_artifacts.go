package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

func migrateTaskArtifacts(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, statement := range []string{
		`CREATE TABLE task_artifacts (
		 id TEXT PRIMARY KEY,
		 task_id TEXT NOT NULL,
		 run_id TEXT NOT NULL,
		 attempt_id TEXT NOT NULL,
		 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
		 revision BIGINT NOT NULL,
		 operation_id TEXT NOT NULL UNIQUE REFERENCES agent_operations(id) ON DELETE RESTRICT,
		 state TEXT NOT NULL CHECK(state IN ('queued','ready','failed','unknown')),
		 paths_json TEXT NOT NULL,
		 manifest_json TEXT,
		 manifest_hash TEXT NOT NULL DEFAULT '',
		 base_sha TEXT NOT NULL DEFAULT '',
		 error TEXT NOT NULL DEFAULT '',
		 created_at TIMESTAMP NOT NULL,
		 UNIQUE(task_id,id), UNIQUE(task_id,revision),
		 FOREIGN KEY(task_id,run_id,attempt_id) REFERENCES task_attempts(task_id,run_id,id) ON DELETE RESTRICT
		)`,
		`CREATE TABLE task_artifact_handoffs (
		 id TEXT PRIMARY KEY,
		 task_id TEXT NOT NULL,
		 artifact_id TEXT NOT NULL,
		 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
		 action TEXT NOT NULL CHECK(action IN ('preflight','apply')),
		 operation_id TEXT NOT NULL UNIQUE REFERENCES agent_operations(id) ON DELETE RESTRICT,
		 state TEXT NOT NULL CHECK(state IN ('queued','ready','failed','unknown')),
		 error TEXT NOT NULL DEFAULT '',
		 created_at TIMESTAMP NOT NULL,
		 FOREIGN KEY(task_id,artifact_id) REFERENCES task_artifacts(task_id,id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX task_artifacts_workspace_idx ON task_artifacts(workspace_id)`,
		`CREATE INDEX task_handoffs_workspace_idx ON task_artifact_handoffs(workspace_id)`,
		`CREATE INDEX task_handoffs_artifact_idx ON task_artifact_handoffs(task_id,artifact_id,created_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
