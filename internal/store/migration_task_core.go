package store

import (
	"context"
	_ "embed"
	"strings"

	"github.com/jmoiron/sqlx"
)

//go:embed task_core.sql
var taskCoreSchema string

func migrateTaskCore(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, statement := range strings.Split(taskCoreSchema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
