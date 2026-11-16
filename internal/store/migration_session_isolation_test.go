package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestSessionIsolationMigration(t *testing.T) {
	db, err := sqlx.Connect("sqlite", filepath.Join(t.TempDir(), "isolation-migration.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	assertSessionIsolationMigration(t, db, "sqlite")
}

func assertSessionIsolationMigration(t *testing.T, db *sqlx.DB, driver string) {
	t.Helper()
	ctx := context.Background()
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:5]); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db, driver: driver}
	server := createOperationTestServer(t, s, "migration-isolation", "migration-isolation-token")
	if _, err := runMigrations(ctx, db, driver, schemaMigrations); err != nil {
		t.Fatal(err)
	}
	row, err := s.Server(ctx, server.ID)
	if err != nil || row.Name != server.Name || row.SessionIsolation != nil {
		t.Fatal("extension migration changed an existing server or implied isolation")
	}
	status := protocol.SessionIsolationStatus{Mode: protocol.IsolationEnabled}
	if err := s.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.3.31", SessionIsolation: &status}); err != nil {
		t.Fatal(err)
	}
	if _, err := runMigrations(ctx, db, driver, schemaMigrations); err != nil {
		t.Fatal(err)
	}
	row, err = s.Server(ctx, server.ID)
	if err != nil || row.SessionIsolation == nil || row.SessionIsolation.Mode != protocol.IsolationEnabled {
		t.Fatal("reopening the upgraded schema lost isolation status")
	}
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM schema_migrations WHERE version=6"); err != nil || count != 1 {
		t.Fatal("isolation migration was not applied exactly once")
	}
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:5]); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatal("a schema 5 control plane was allowed to open schema 6")
	}
}
