package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

var retiredSessionTables = map[string]bool{
	"codex_scheduled_tasks": true, "gemini_scheduled_tasks": true, "claude_scheduled_tasks": true,
	"server_gemini_credential_profiles": true, "server_claude_credential_profiles": true, "server_claude_cli": true,
}

func retiredSessionColumn(table, column string) bool {
	return table == "servers" && (column == "codex_version" || column == "codex_ready" || column == "gemini_version" || column == "gemini_ready") || table == "server_credential_profiles" && column == "codex_profile_id"
}

func TestSessionCleanupMigration(t *testing.T) {
	db, err := sqlx.Connect("sqlite", filepath.Join(t.TempDir(), "cleanup.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	assertSessionCleanupMigration(t, db, "sqlite")
}

// Historical-state tests must construct schema 4 before injecting old rows.
// Re-running the frozen baseline on a contracted database would recreate copies.
func openPreCleanupTestStore(dsn string) (*Store, error) {
	driver := "sqlite"
	if strings.HasPrefix(dsn, "postgres") {
		driver = "pgx"
	}
	db, err := sqlx.Connect(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = runMigrations(context.Background(), db, driver, schemaMigrations[:4]); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, driver: driver}, nil
}

func sessionCleanupTables(t *testing.T, db *sqlx.DB, driver string) []string {
	t.Helper()
	query := `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	if driver == "pgx" {
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema=current_schema() AND table_type='BASE TABLE' ORDER BY table_name`
	}
	var tables []string
	if err := db.Select(&tables, query); err != nil {
		t.Fatal(err)
	}
	return tables
}
func sessionCleanupSnapshot(t *testing.T, db *sqlx.DB, driver string) map[string][]string {
	t.Helper()
	snapshot := map[string][]string{}
	for _, table := range sessionCleanupTables(t, db, driver) {
		if retiredSessionTables[table] || table == "schema_migrations" {
			continue
		}
		rows, err := db.Queryx("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		values := []string{}
		for rows.Next() {
			row := map[string]any{}
			if err := rows.MapScan(row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for key := range row {
				if retiredSessionColumn(table, key) {
					delete(row, key)
				}
			}
			raw, err := json.Marshal(row)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			values = append(values, string(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(values)
		snapshot[table] = values
	}
	return snapshot
}
func assertSessionCleanupMigration(t *testing.T, db *sqlx.DB, driver string) {
	t.Helper()
	ctx := context.Background()
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:4]); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO servers(id,name,codex_version,gemini_version) VALUES('cleanup-server','Synthetic','stale-codex','stale-gemini')`,
		`INSERT INTO projects(id,name) VALUES('cleanup-project','Synthetic')`,
		`INSERT INTO workspaces(id,project_id,server_id,path) VALUES('cleanup-workspace','cleanup-project','cleanup-server','/synthetic')`,
		`INSERT INTO credential_profiles(id,kind,name,endpoint,ciphertext) VALUES('cleanup-codex','codex','Synthetic Codex','','fixture-only'),('cleanup-git','git','Synthetic Git','','fixture-only'),('cleanup-gemini','gemini','Synthetic Gemini','','fixture-only'),('cleanup-claude','claude','Synthetic Claude','','fixture-only')`,
		`INSERT INTO server_credential_profiles(server_id,codex_profile_id,git_profile_id) VALUES('cleanup-server','cleanup-codex','cleanup-git')`,
		`INSERT INTO server_git_credential_profiles(server_id,profile_id,position) VALUES('cleanup-server','cleanup-git',0)`,
		`INSERT INTO server_gemini_credential_profiles(server_id,profile_id,profile_revision) VALUES('cleanup-server','cleanup-gemini','stale-revision')`,
		`INSERT INTO server_claude_credential_profiles(server_id,profile_id,profile_revision,model) VALUES('cleanup-server','cleanup-claude','stale-revision','stale-model')`,
		`INSERT INTO server_claude_cli(server_id,version,ready) VALUES('cleanup-server','stale-claude',0)`,
		`INSERT INTO agent_operations(id,server_id,kind,payload,status,idempotency_key) VALUES('cleanup-op','cleanup-server','credentials.configure','{}','succeeded','cleanup-operation-key')`,
		`INSERT INTO server_credential_updates(operation_id,server_id,codex_profile_id,git_profile_id) VALUES('cleanup-op','cleanup-server','cleanup-codex','cleanup-git')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, engine := range []string{"codex", "gemini", "claude"} {
		statements := []string{
			fmt.Sprintf(`INSERT INTO %s_threads(id,workspace_id,title) VALUES('shared-native-id','cleanup-workspace','Synthetic %s')`, engine, engine),
			fmt.Sprintf(`INSERT INTO %s_scheduled_tasks(id,thread_id,server_id,name,prompt,schedule,next_run_at) VALUES('task-%s','shared-native-id','cleanup-server','Stale task','Stale prompt','@daily',CURRENT_TIMESTAMP)`, engine, engine),
			fmt.Sprintf(`INSERT INTO scheduled_tasks(engine,id,thread_id,server_id,name,prompt,schedule,next_run_at) VALUES('%s','task-%s','shared-native-id','cleanup-server','Current task','Current prompt','@weekly',CURRENT_TIMESTAMP)`, engine, engine),
			fmt.Sprintf(`INSERT INTO server_engines(server_id,engine,cli_version,ready,credential_profile_id,profile_revision,model,updated_at) VALUES('cleanup-server','%s','current-cli',1,'cleanup-%s','current-revision','current-model',CURRENT_TIMESTAMP)`, engine, engine),
		}
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := sessionCleanupSnapshot(t, db, driver)
	// Execute every destructive statement and then fail: both databases must roll
	// the dropped structures back together with migration history and live data.
	injected := append([]migration(nil), schemaMigrations...)
	injected[len(injected)-1].up = func(ctx context.Context, tx *sqlx.Tx, driver string) error {
		if err := migrateSessionCleanup(ctx, tx, driver); err != nil {
			return err
		}
		return errors.New("injected cleanup failure")
	}
	if _, err := runMigrations(ctx, db, driver, injected); err == nil || !strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("rollback probe: %v", err)
	}
	tables := sessionCleanupTables(t, db, driver)
	for table := range retiredSessionTables {
		found := false
		for _, name := range tables {
			found = found || name == table
		}
		if !found {
			t.Fatalf("rollback lost %s", table)
		}
	}
	if got := sessionCleanupSnapshot(t, db, driver); !reflect.DeepEqual(before, got) {
		t.Fatal("failed cleanup changed retained data")
	}
	for range 2 {
		if _, err := runMigrations(ctx, db, driver, schemaMigrations); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range sessionCleanupTables(t, db, driver) {
		if retiredSessionTables[table] {
			t.Fatalf("retired table survived: %s", table)
		}
		rows, err := db.Query("SELECT * FROM " + table + " LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			if retiredSessionColumn(table, column) {
				t.Fatalf("retired column survived: %s.%s", table, column)
			}
		}
	}
	if got := sessionCleanupSnapshot(t, db, driver); !reflect.DeepEqual(before, got) {
		t.Fatal("cleanup changed retained data")
	}
	var versions int
	if err := db.Get(&versions, "SELECT COUNT(*) FROM schema_migrations WHERE version=5"); err != nil || versions != 1 {
		t.Fatalf("migration history: %d %v", versions, err)
	}
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:4]); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("old control plane accepted contracted schema: %v", err)
	}
	s := &Store{DB: db, driver: driver}
	if err := s.Heartbeat(ctx, "cleanup-server", protocol.Heartbeat{AgentVersion: "0.3.26", Capabilities: testCapabilities("new-cli", "new-cli"), CodexVersion: "new-cli", GeminiVersion: "new-cli", ClaudeVersion: "new-cli"}); err != nil {
		t.Fatal(err)
	}
	server, err := s.Server(ctx, "cleanup-server")
	if err != nil || server.CodexVersion != "new-cli" || server.GitProfileID != "cleanup-git" || server.ClaudeProfileModel != "current-model" {
		t.Fatalf("live engine/Git projection: %+v %v", server, err)
	}
}
