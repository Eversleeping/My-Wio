package store

import (
	"context"
	"github.com/jmoiron/sqlx"
)

func migrateServerPlatforms(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, statement := range []string{`CREATE TABLE server_platforms (server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,os TEXT NOT NULL,architecture TEXT NOT NULL)`, `ALTER TABLE server_registrations ADD COLUMN repair_server_id TEXT NOT NULL DEFAULT ''`} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
