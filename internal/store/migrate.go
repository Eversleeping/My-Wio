package store

import (
	"context"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/buildinfo"
)

type migration struct {
	version  int
	name     string
	up       func(context.Context, *sqlx.Tx, string) error
	baseline func(context.Context, *sqlx.DB, string) error
}

// Append new migrations. Published entries and the baseline are immutable.
var schemaMigrations = []migration{
	{version: 1, name: "baseline", baseline: migrateBaseline},
}

// PostgreSQL advisory locks are scoped to a database. All Wio schemas in one
// database share this stable key, including control planes using older lists.
const migrationLockKey int64 = 0x57494f4d49475241

// runMigrations reports whether the legacy baseline (including its startup
// maintenance) ran, so Open need not repeat that maintenance on a fresh upgrade.
func runMigrations(ctx context.Context, db *sqlx.DB, driver string, migrations []migration) (bool, error) {
	last := 0
	for _, step := range migrations {
		if step.version <= last || step.name == "" || (step.up == nil) == (step.baseline == nil) || (step.baseline != nil && step.version != 1) {
			return false, fmt.Errorf("invalid migration %d %q", step.version, step.name)
		}
		last = step.version
	}
	baselineApplied := false
	err := withMigrationLock(ctx, db, driver, func() error {
		if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL
		)`); err != nil {
			return fmt.Errorf("create migration history: %w", err)
		}
		var versions []int
		if err := db.SelectContext(ctx, &versions, "SELECT version FROM schema_migrations ORDER BY version"); err != nil {
			return fmt.Errorf("read migration history: %w", err)
		}
		if len(versions) > 0 && versions[len(versions)-1] > last {
			return fmt.Errorf("database schema version %d is newer than supported schema version %d (control plane %s); use a control plane supporting schema version %d or later", versions[len(versions)-1], last, buildinfo.Version, versions[len(versions)-1])
		}
		applied := make(map[int]bool, len(versions))
		for _, version := range versions {
			applied[version] = true
		}
		for _, step := range migrations {
			if applied[step.version] {
				continue
			}
			if err := applyMigration(ctx, db, driver, step); err != nil {
				return fmt.Errorf("migration %d %q: %w", step.version, step.name, err)
			}
			baselineApplied = baselineApplied || step.baseline != nil
		}
		return nil
	})
	return baselineApplied, err
}

func applyMigration(ctx context.Context, db *sqlx.DB, driver string, step migration) error {
	query := db.Rebind("INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)")
	if step.baseline != nil {
		if err := step.baseline(ctx, db, driver); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, query, step.version, step.name, time.Now().UTC())
		return err
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := step.up(ctx, tx, driver); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, query, step.version, step.name, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func withMigrationLock(ctx context.Context, db *sqlx.DB, driver string, migrate func() error) (err error) {
	if driver != "pgx" {
		return migrate()
	}
	// No Store has been published yet. Reserve the lock session without taking
	// capacity away from the legacy DB-based baseline, even with a pool of one.
	limit := db.Stats().MaxOpenConnections
	if limit > 0 {
		db.SetMaxOpenConns(limit + 1)
		defer db.SetMaxOpenConns(limit)
	}
	conn, err := db.Connx(ctx)
	if err != nil {
		return fmt.Errorf("open migration lock connection: %w", err)
	}
	defer conn.Close()
	discard := func() { _ = conn.Raw(func(any) error { return sqldriver.ErrBadConn }) }
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		// A canceled call may have acquired its lock on the server. Discard the
		// physical connection rather than returning a possibly locked session.
		discard()
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.GetContext(unlockCtx, &unlocked, "SELECT pg_advisory_unlock($1)", migrationLockKey)
		if unlockErr == nil && !unlocked {
			unlockErr = errors.New("migration lock was not held")
		}
		if unlockErr != nil {
			discard()
			err = errors.Join(err, fmt.Errorf("release migration lock: %w", unlockErr))
		}
	}()
	return migrate()
}
