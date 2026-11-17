package store

import (
	"context"
	"github.com/jmoiron/sqlx"
)

// Published schema 6 is retained for upgrade and rollback compatibility.
// The retired table is no longer read or written by the application.
func migrateSessionIsolation(ctx context.Context, tx *sqlx.Tx, _ string) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE server_session_isolation (
 server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 status TEXT NOT NULL
 )`)
	return err
}
