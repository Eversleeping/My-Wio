package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/buildinfo"
)

func TestMigrationRunner(t *testing.T) {
	db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "migrations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	assertMigrationRunner(t, db, "sqlite")
}

// Shared with the PostgreSQL integration entry point, so every transactional
// migration and failure assertion is exercised against both database engines.
func assertMigrationRunner(t *testing.T, db *sqlx.DB, driver string) {
	t.Helper()
	ctx := context.Background()
	baselineCalls, transactionalCalls := 0, 0
	steps := []migration{
		{version: 1, name: "test_baseline", baseline: func(ctx context.Context, db *sqlx.DB, _ string) error {
			baselineCalls++
			_, err := db.ExecContext(ctx, "CREATE TABLE migration_probe(value INTEGER NOT NULL)")
			return err
		}},
		{version: 2, name: "test_data", up: func(ctx context.Context, tx *sqlx.Tx, _ string) error {
			transactionalCalls++
			_, err := tx.ExecContext(ctx, "INSERT INTO migration_probe(value) VALUES(42)")
			return err
		}},
	}
	for attempt := 0; attempt < 2; attempt++ {
		baseline, err := runMigrations(ctx, db, driver, steps)
		if err != nil || baseline != (attempt == 0) {
			t.Fatalf("run %d: baseline=%v err=%v", attempt, baseline, err)
		}
	}
	var value, records int
	if err := db.GetContext(ctx, &value, "SELECT value FROM migration_probe"); err != nil || value != 42 {
		t.Fatalf("migration data=%d, error=%v", value, err)
	}
	if err := db.GetContext(ctx, &records, "SELECT COUNT(*) FROM schema_migrations"); err != nil || records != 2 || baselineCalls != 1 || transactionalCalls != 1 {
		t.Fatalf("history=%d baseline calls=%d transactional calls=%d, error=%v", records, baselineCalls, transactionalCalls, err)
	}
	var appliedAt time.Time
	if err := db.GetContext(ctx, &appliedAt, "SELECT applied_at FROM schema_migrations WHERE version=1"); err != nil || appliedAt.IsZero() {
		t.Fatalf("invalid application timestamp: %v %v", appliedAt, err)
	}

	failure := errors.New("injected migration failure")
	failing := append(append([]migration(nil), steps...), migration{version: 3, name: "rollback_probe", up: func(ctx context.Context, tx *sqlx.Tx, _ string) error {
		if _, err := tx.ExecContext(ctx, "CREATE TABLE migration_rolled_back(value INTEGER)"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE migration_probe SET value=99"); err != nil {
			return err
		}
		return failure
	}})
	_, err := runMigrations(ctx, db, driver, failing)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), `migration 3 "rollback_probe"`) {
		t.Fatalf("missing migration identity or cause: %v", err)
	}
	if err := db.GetContext(ctx, &records, "SELECT COUNT(*) FROM schema_migrations WHERE version=3"); err != nil || records != 0 {
		t.Fatalf("failed migration was recorded: %d %v", records, err)
	}
	if err := db.GetContext(ctx, &value, "SELECT value FROM migration_probe"); err != nil || value != 42 {
		t.Fatalf("failed migration retained data writes: %d %v", value, err)
	}
	query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='migration_rolled_back'"
	if driver == "pgx" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='migration_rolled_back'"
	}
	if err := db.GetContext(ctx, &records, query); err != nil || records != 0 {
		t.Fatalf("failed migration retained schema writes: %d %v", records, err)
	}
	// Fix the failed entry, retry, then prove the successful transaction's row
	// and data are both durable and never repeated on another open.
	failing[2].up = func(ctx context.Context, tx *sqlx.Tx, _ string) error {
		_, err := tx.ExecContext(ctx, "UPDATE migration_probe SET value=value+1")
		return err
	}
	for range 2 {
		if _, err := runMigrations(ctx, db, driver, failing); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.GetContext(ctx, &value, "SELECT value FROM migration_probe"); err != nil || value != 43 {
		t.Fatalf("retry was not exactly once: %d %v", value, err)
	}
}

func TestOpenRecordsBaselineAndKeepsStartupMaintenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wio.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var name string
	if err := db.DB.QueryRowx("SELECT version,name FROM schema_migrations").Scan(&version, &name); err != nil || version != 1 || name != "baseline" {
		t.Fatalf("baseline history: %d %q %v", version, name, err)
	}
	// A baseline-only index is deliberately removed. Re-running schema.sql
	// would recreate it, so its absence distinguishes a real versioned reopen.
	if _, err := db.DB.Exec("DROP INDEX events_stream_idx"); err != nil {
		t.Fatal(err)
	}
	server := createOperationTestServer(t, db, "migration-maintenance", "migration-maintenance-token")
	if _, err := db.DB.Exec(db.Q("UPDATE servers SET revoked_at=? WHERE id=?"), time.Now().UTC(), server.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.DB.Get(&count, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='events_stream_idx'"); err != nil || count != 0 {
		t.Fatalf("baseline ran again: %d %v", count, err)
	}
	if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM servers WHERE id=?"), server.ID); err != nil || count != 0 {
		t.Fatalf("startup maintenance was skipped: %d %v", count, err)
	}
}

func TestMigrationRegistryValidation(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	up := func(context.Context, *sqlx.Tx, string) error { return nil }
	for _, steps := range [][]migration{
		{{version: 0, name: "zero", up: up}},
		{{version: 1, name: "empty_callback"}},
		{{version: 1, up: up}},
		{{version: 2, name: "second", up: up}, {version: 1, name: "first", up: up}},
		{{version: 1, name: "first", up: up}, {version: 1, name: "duplicate", up: up}},
	} {
		if _, err := runMigrations(context.Background(), db, "sqlite", steps); err == nil {
			t.Fatalf("invalid registry accepted: %+v", steps)
		}
	}
}

func TestOpenRejectsFutureSchema(t *testing.T) {
	assertOpenRejectsFutureSchema(t, filepath.Join(t.TempDir(), "future.db"))
}

// Exercise the public startup path on both engines. Missing baseline history
// and a revoked server distinguish early refusal from migration or maintenance.
func assertOpenRejectsFutureSchema(t *testing.T, dsn string) {
	t.Helper()
	database, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var history []struct {
		Version   int
		Name      string
		AppliedAt time.Time `db:"applied_at"`
	}
	if err := database.DB.Select(&history, "SELECT version,name,applied_at FROM schema_migrations ORDER BY version"); err != nil {
		t.Fatal(err)
	}
	supported := schemaMigrations[len(schemaMigrations)-1].version
	future := supported + 100
	server := createOperationTestServer(t, database, "future-schema", "future-schema-token")
	for _, statement := range []string{"DELETE FROM schema_migrations", "DROP INDEX events_stream_idx"} {
		if _, err := database.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.DB.Exec(database.Q("INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)"), future, "future", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(database.Q("UPDATE servers SET revoked_at=? WHERE id=?"), time.Now().UTC(), server.ID); err != nil {
		t.Fatal(err)
	}
	rejected, err := Open(dsn)
	if rejected != nil {
		rejected.Close()
		t.Fatal("future database unexpectedly opened")
	}
	expected := fmt.Sprintf("database schema version %d is newer than supported schema version %d (control plane %s); use a control plane supporting schema version %d or later", future, supported, buildinfo.Version, future)
	if err == nil || err.Error() != expected {
		t.Fatalf("downgrade error: got %v, want %q", err, expected)
	}
	var records int
	if err := database.DB.Get(&records, "SELECT COUNT(*) FROM schema_migrations"); err != nil || records != 1 {
		t.Fatalf("refusal changed migration history: %d %v", records, err)
	}
	if err := database.DB.Get(&records, database.Q("SELECT COUNT(*) FROM servers WHERE id=?"), server.ID); err != nil || records != 1 {
		t.Fatalf("refusal ran startup maintenance: %d %v", records, err)
	}
	query := "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='events_stream_idx'"
	if database.driver == "pgx" {
		query = "SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='events_stream_idx'"
	}
	if err := database.DB.Get(&records, query); err != nil || records != 0 {
		t.Fatalf("refusal ran baseline: %d %v", records, err)
	}
	if _, err := database.DB.Exec("DELETE FROM schema_migrations"); err != nil {
		t.Fatal(err)
	}
	// Restore the actual supported history, including future additions to the
	// registry; recreating a baseline-only history would rerun later migrations.
	for _, record := range history {
		if _, err := database.DB.Exec(database.Q("INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)"), record.Version, record.Name, record.AppliedAt); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(dsn)
	if err != nil {
		t.Fatalf("supported database cannot reopen after refusal: %v", err)
	}
	defer reopened.Close()
}
