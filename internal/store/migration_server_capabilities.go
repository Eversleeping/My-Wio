package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// Version 4 adds an optional declaration without changing the servers row or
// replacing any engine configuration. Missing rows identify legacy Agents.
func migrateServerCapabilities(ctx context.Context, tx *sqlx.Tx, _ string) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE server_capabilities (
 server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 capabilities TEXT NOT NULL
 )`)
	return err
}
