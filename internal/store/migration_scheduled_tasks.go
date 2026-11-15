package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// Version 2 expands storage without dropping or writing the legacy tables.
func migrateScheduledTasks(ctx context.Context, tx *sqlx.Tx, _ string) error {
	var conflicts []string
	if err := tx.SelectContext(ctx, &conflicts, `SELECT id FROM (
 SELECT id FROM codex_scheduled_tasks UNION ALL SELECT id FROM gemini_scheduled_tasks UNION ALL SELECT id FROM claude_scheduled_tasks
 ) tasks GROUP BY id HAVING COUNT(*)>1 ORDER BY id`); err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("duplicate scheduled task IDs: %s", strings.Join(conflicts, ", "))
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE scheduled_tasks (
  engine TEXT NOT NULL CHECK(engine IN ('codex','gemini','claude')),
  thinking_level TEXT NOT NULL DEFAULT '',
  id TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL,
  server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prompt TEXT NOT NULL,
  schedule TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  enabled INTEGER NOT NULL DEFAULT 1,
  model TEXT NOT NULL DEFAULT '',
  reasoning_effort TEXT NOT NULL DEFAULT '',
  approval_mode TEXT NOT NULL DEFAULT 'on-request',
  next_run_at TIMESTAMP NOT NULL,
  last_run_at TIMESTAMP,
  last_run_status TEXT NOT NULL DEFAULT '',
  last_run_message TEXT NOT NULL DEFAULT '',
  last_operation_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`); err != nil {
		return err
	}
	for _, engine := range []string{"codex", "gemini", "claude"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_tasks(engine,id,thread_id,server_id,name,prompt,schedule,timezone,enabled,model,reasoning_effort,approval_mode,thinking_level,next_run_at,last_run_at,last_run_status,last_run_message,last_operation_id,created_at,updated_at) SELECT '`+engine+`',id,thread_id,server_id,name,prompt,schedule,timezone,enabled,model,reasoning_effort,approval_mode,thinking_level,next_run_at,last_run_at,last_run_status,last_run_message,last_operation_id,created_at,updated_at FROM `+engine+`_scheduled_tasks`); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		"CREATE INDEX scheduled_tasks_due_idx ON scheduled_tasks(enabled,next_run_at)",
		"CREATE INDEX scheduled_tasks_thread_idx ON scheduled_tasks(engine,thread_id)",
		"CREATE INDEX scheduled_tasks_operation_idx ON scheduled_tasks(last_operation_id)",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
