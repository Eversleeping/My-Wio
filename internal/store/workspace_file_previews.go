package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/jmoiron/sqlx"
)

func migrateWorkspaceFilePreviewMedia(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		_, err := db.ExecContext(ctx, `ALTER TABLE workspace_file_previews
			ADD COLUMN IF NOT EXISTS mime_type TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS encoding TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return fmt.Errorf("migrate workspace file preview media: %w", err)
		}
		return nil
	}
	var columns []string
	if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('workspace_file_previews')"); err != nil {
		return fmt.Errorf("inspect workspace file preview columns: %w", err)
	}
	for _, column := range []string{"mime_type", "encoding"} {
		if !slices.Contains(columns, column) {
			if _, err := db.ExecContext(ctx, "ALTER TABLE workspace_file_previews ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("migrate workspace file preview %s: %w", column, err)
			}
		}
	}
	return nil
}
