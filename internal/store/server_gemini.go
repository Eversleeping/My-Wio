package store

import (
	"context"
	"github.com/jmoiron/sqlx"
)

func migrateServerGemini(ctx context.Context, db *sqlx.DB, driver string) error {
	for _, column := range []struct{ name, definition string }{
		{"gemini_version", "TEXT NOT NULL DEFAULT ''"},
		{"gemini_ready", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if driver == "pgx" {
			if _, err := db.ExecContext(ctx, "ALTER TABLE servers ADD COLUMN IF NOT EXISTS "+column.name+" "+column.definition); err != nil {
				return err
			}
		} else {
			var count int
			if err := db.GetContext(ctx, &count, "SELECT count(*) FROM pragma_table_info('servers') WHERE name=?", column.name); err != nil {
				return err
			}
			if count == 0 {
				if _, err := db.ExecContext(ctx, "ALTER TABLE servers ADD COLUMN "+column.name+" "+column.definition); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
