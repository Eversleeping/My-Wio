package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// A new binding or default model invalidates the saved model choice. Editing
// unrelated preset metadata does not. No credential material is exposed.
const claudeModelRevision = `CASE WHEN ccp.profile_id IS NULL THEN '' ELSE ccp.profile_id || ':' || CAST(ccp.updated_at AS TEXT) || ':' || COALESCE(claude.model,'') END`

const claudeWorkspaceModelRevision = `COALESCE((SELECT ` + claudeModelRevision + ` FROM workspaces w LEFT JOIN server_claude_credential_profiles ccp ON ccp.server_id=w.server_id LEFT JOIN credential_profiles claude ON claude.id=ccp.profile_id WHERE w.id=claude_threads.workspace_id),'')`

func migrateClaudeModelConfig(ctx context.Context, db *sqlx.DB, driver string) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	query := "SELECT COUNT(*) FROM pragma_table_info('claude_threads') WHERE name='model_credential_revision'"
	if driver == "pgx" {
		query = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='claude_threads' AND column_name='model_credential_revision'"
	}
	if err := tx.GetContext(ctx, &count, query); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE claude_threads ADD COLUMN model_credential_revision TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		// Backfill once: preserve choices made after the configuration changed,
		// while allowing old sessions to adopt a newer default on their next entry.
		if _, err := tx.ExecContext(ctx, `UPDATE claude_threads SET model_credential_revision=COALESCE((SELECT `+claudeModelRevision+` FROM workspaces w JOIN server_claude_credential_profiles ccp ON ccp.server_id=w.server_id JOIN credential_profiles claude ON claude.id=ccp.profile_id WHERE w.id=claude_threads.workspace_id AND ccp.updated_at<=claude_threads.updated_at AND claude.updated_at<=claude_threads.updated_at),'')`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
