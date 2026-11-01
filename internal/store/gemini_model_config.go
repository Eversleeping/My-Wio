package store

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

// Only binding metadata contributes to the revision; no key or key hash is exposed.
const geminiCredentialRevision = `CASE WHEN gcp.profile_id IS NULL THEN '' ELSE gcp.profile_id || ':' || CAST(gcp.updated_at AS TEXT) END`

type GeminiModelConfig struct {
	ProfileRevision string    `db:"profile_revision"`
	Revision        string    `db:"revision"`
	ProfileID       string    `db:"profile_id"`
	AppliedAt       time.Time `db:"updated_at"`
}

func (s *Store) GeminiModelConfiguration(ctx context.Context, serverID string) (GeminiModelConfig, error) {
	var config GeminiModelConfig
	err := s.DB.GetContext(ctx, &config, s.Q(`SELECT `+geminiCredentialRevision+` AS revision,COALESCE(gcp.profile_id,'') AS profile_id,gcp.updated_at,gcp.profile_revision FROM server_gemini_credential_profiles gcp WHERE gcp.server_id=?`), serverID)
	return config, err
}

func migrateGeminiModelConfig(ctx context.Context, db *sqlx.DB, driver string) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	threadColumnAdded := false
	for _, column := range []struct{ table, name string }{
		{"gemini_threads", "model_credential_revision"},
		{"server_gemini_credential_profiles", "profile_revision"},
		{"server_gemini_credential_updates", "profile_revision"},
	} {
		var count int
		query := "SELECT COUNT(*) FROM pragma_table_info('" + column.table + "') WHERE name='" + column.name + "'"
		if driver == "pgx" {
			query = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='" + column.table + "' AND column_name='" + column.name + "'"
		}
		if err = tx.GetContext(ctx, &count, query); err != nil {
			return err
		}
		if count == 0 {
			if _, err = tx.ExecContext(ctx, "ALTER TABLE "+column.table+" ADD COLUMN "+column.name+" TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
			if column.table == "gemini_threads" {
				threadColumnAdded = true
			}
		}
	}
	if threadColumnAdded {
		// Preserve legacy custom models unless credentials were applied after the
		// session's last update. Run this backfill only when adding the column.
		if _, err = tx.ExecContext(ctx, `UPDATE gemini_threads SET model_credential_revision=COALESCE((SELECT `+geminiCredentialRevision+` FROM workspaces w JOIN server_gemini_credential_profiles gcp ON gcp.server_id=w.server_id WHERE w.id=gemini_threads.workspace_id AND gcp.updated_at<=gemini_threads.updated_at),'')`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
