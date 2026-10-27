package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

func migrateMetricCapacity(ctx context.Context, db *sqlx.DB, driver string) error {
	var columns []string
	if driver != "pgx" {
		if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('metric_rollups')"); err != nil {
			return fmt.Errorf("inspect metric capacity columns: %w", err)
		}
	}
	for _, name := range []string{"memory_used_bytes", "memory_total_bytes", "disk_used_bytes", "disk_total_bytes"} {
		statement := "ALTER TABLE metric_rollups ADD COLUMN "
		if driver == "pgx" {
			statement += "IF NOT EXISTS "
		} else {
			found := false
			for _, column := range columns {
				found = found || column == name
			}
			if found {
				continue
			}
		}
		if _, err := db.ExecContext(ctx, statement+name+" BIGINT NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("migrate metric capacity columns: %w", err)
		}
	}
	return nil
}
