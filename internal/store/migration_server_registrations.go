package store

import (
	"context"
	"github.com/jmoiron/sqlx"
)

func migrateServerRegistrations(ctx context.Context, tx *sqlx.Tx, _ string) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE server_registrations (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 code_hash TEXT NOT NULL,
 device_hash TEXT NOT NULL DEFAULT '',
 config_json TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending',
 server_id TEXT NOT NULL DEFAULT '',
 progress_json TEXT NOT NULL DEFAULT '[]',
 result_json TEXT NOT NULL DEFAULT '{}',
 error_code TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMP NOT NULL,
 expires_at TIMESTAMP NOT NULL,
 session_expires_at TIMESTAMP NOT NULL
 )`)
	return err
}
