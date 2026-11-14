//go:build postgresintegration

package store

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func assertPostgresMigrationRunner(t *testing.T, databaseURL string) {
	t.Helper()
	admin, err := sqlx.Connect("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	t.Run("future schema refusal", func(t *testing.T) {
		if _, err := admin.Exec("CREATE SCHEMA future_schema"); err != nil {
			t.Fatal(err)
		}
		assertOpenRejectsFutureSchema(t, postgresSchemaURL(t, databaseURL, "future_schema"))
	})
	if _, err := admin.Exec("CREATE SCHEMA migration_runner"); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("search_path", "migration_runner")
	dsn.RawQuery = query.Encode()
	db, err := sqlx.Connect("pgx", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	assertMigrationRunner(t, db, "pgx")
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("migration changed the configured pool limit: %d", got)
	}
	if _, err := admin.Exec("CREATE SCHEMA migration_concurrency"); err != nil {
		t.Fatal(err)
	}
	query.Set("search_path", "migration_concurrency")
	dsn.RawQuery = query.Encode()
	first, err := sqlx.Connect("pgx", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := sqlx.Connect("pgx", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.SetMaxOpenConns(1)
	second.SetMaxOpenConns(1)
	var secondPID int
	if err := second.Get(&secondPID, "SELECT pg_backend_pid()"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(release) })
	var workers sync.WaitGroup
	defer func() { releaseFirst(); cancel(); workers.Wait() }()
	var calls atomic.Int32
	steps := []migration{{version: 1, name: "concurrent_baseline", baseline: func(ctx context.Context, db *sqlx.DB, _ string) error {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		_, err := db.ExecContext(ctx, "CREATE TABLE concurrent_probe(value INTEGER)")
		return err
	}}}
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); _, err := runMigrations(ctx, first, "pgx", steps); firstDone <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	workers.Add(1)
	go func() { defer workers.Done(); _, err := runMigrations(ctx, second, "pgx", steps); secondDone <- err }()
	// Observe the second physical session waiting for the advisory lock before
	// releasing the first. This proves serialization without a scheduling guess.
	for {
		var waiting bool
		if err := admin.GetContext(ctx, &waiting, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND wait_event='advisory')", secondPID); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-secondDone:
			t.Fatalf("concurrent migrator did not wait for the lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// A canceled lock acquisition must discard any uncertain session lock.
	blockedCtx, blockedCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, blockedErr := runMigrations(blockedCtx, db, "pgx", steps)
	blockedCancel()
	if !errors.Is(blockedErr, context.DeadlineExceeded) {
		t.Fatalf("canceled lock acquisition: %v", blockedErr)
	}
	releaseFirst()
	for _, result := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent baseline ran %d times", calls.Load())
	}
	// The connection pool used for the canceled acquisition remains usable.
	if err := withMigrationLock(ctx, db, "pgx", func() error { return nil }); err != nil {
		t.Fatalf("migration lock leaked after cancellation: %v", err)
	}
}
