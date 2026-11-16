package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// Version 5 contracts the copies replaced by versions 2 and 3. Native thread
// storage, Git bindings and credential-update snapshots are still in use.
// Do not CASCADE: an unexpected dependency must abort the entire migration.
func migrateSessionCleanup(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, statement := range []string{
		"DROP TABLE codex_scheduled_tasks",
		"DROP TABLE gemini_scheduled_tasks",
		"DROP TABLE claude_scheduled_tasks",
		"DROP TABLE server_gemini_credential_profiles",
		"DROP TABLE server_claude_credential_profiles",
		"DROP TABLE server_claude_cli",
		"ALTER TABLE servers DROP COLUMN codex_version",
		"ALTER TABLE servers DROP COLUMN codex_ready",
		"ALTER TABLE servers DROP COLUMN gemini_version",
		"ALTER TABLE servers DROP COLUMN gemini_ready",
		"ALTER TABLE server_credential_profiles DROP COLUMN codex_profile_id",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
