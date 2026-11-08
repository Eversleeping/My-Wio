//go:build postgresintegration

package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresAuthConsumptionIsAtomic exercises the migration and the
// single-use guarantees where they matter: on real concurrent connections.
func TestPostgresAuthConsumptionIsAtomic(t *testing.T) {
	root := t.TempDir()
	var postgresLog bytes.Buffer
	config := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(availablePostgresPort(t)).
		Database("wio_auth").
		Username("wio_auth").
		Password("wio_auth").
		RuntimePath(filepath.Join(root, "runtime")).
		DataPath(filepath.Join(root, "data")).
		StartTimeout(60 * time.Second).
		Logger(&postgresLog)
	postgres := embeddedpostgres.NewDatabase(config)
	if err := postgres.Start(); err != nil {
		t.Fatalf("start embedded PostgreSQL: %v\n%s", err, postgresLog.String())
	}
	t.Cleanup(func() {
		if err := postgres.Stop(); err != nil {
			t.Errorf("stop embedded PostgreSQL: %v\n%s", err, postgresLog.String())
		}
	})
	databaseURL := config.GetConnectionURL() + "?sslmode=disable"

	legacy, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE users (id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,totp_secret TEXT NOT NULL,recovery_hashes TEXT NOT NULL,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes) VALUES('legacy-user','legacy-admin','unused','encrypted','["keep"]')`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	// Migration must apply to the existing database and be repeatable.
	for attempt := 1; attempt <= 2; attempt++ {
		database, err := Open(databaseURL)
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
	database, err := Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUserTOTPCounter(context.Background(), database.DB, database.driver); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}

	ctx := context.Background()
	var columnType string
	if err := database.DB.GetContext(ctx, &columnType, "SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='totp_last_counter'"); err != nil || columnType != "bigint" {
		t.Fatalf("totp_last_counter column: %q %v", columnType, err)
	}
	var counter int64
	if err := database.DB.GetContext(ctx, &counter, "SELECT totp_last_counter FROM users WHERE id='legacy-user'"); err != nil || counter != 0 {
		t.Fatalf("legacy counter: %d %v", counter, err)
	}
	if advanced, err := database.AdvanceTOTPCounter(ctx, "legacy-user", 7); err != nil || !advanced {
		t.Fatalf("advance migrated user: %v %v", advanced, err)
	}
	if consumed, err := database.ConsumeRecoveryCode(ctx, "legacy-user", "keep"); err != nil || !consumed {
		t.Fatalf("consume migrated recovery code: %v %v", consumed, err)
	}

	assertTOTPCounterMonotonic(t, database)
	assertRecoveryCodesSingleUse(t, database)
	assertConcurrentSingleUse(t, database)
	assertProjectNameFilterSemantics(t, database)
}
