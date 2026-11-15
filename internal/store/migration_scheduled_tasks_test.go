package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func TestScheduledTaskMigration(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "preserves all fields"
		if conflict {
			name = "conflict rolls back and retries"
		}
		t.Run(name, func(t *testing.T) {
			db, err := sqlx.Connect("sqlite", filepath.Join(t.TempDir(), "migration.db")+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			assertScheduledTaskMigration(t, db, "sqlite", conflict)
		})
	}
}

// Also called by the PostgreSQL integration entry point.
func assertScheduledTaskMigration(t *testing.T, db *sqlx.DB, driver string, conflict bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:1]); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db, driver: driver}
	for _, statement := range []string{
		"INSERT INTO servers(id,name) VALUES('server','Migration')",
		"INSERT INTO projects(id,name) VALUES('project','Migration')",
		"INSERT INTO workspaces(id,project_id,server_id,path) VALUES('workspace','project','server','/fixture')",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 15, 1, 2, 3, 0, time.UTC)
	const columns = "id,thread_id,server_id,name,prompt,schedule,timezone,enabled,model,reasoning_effort,approval_mode,thinking_level,next_run_at,last_run_at,last_run_status,last_run_message,last_operation_id,created_at,updated_at"
	for index, engine := range []string{"codex", "gemini", "claude"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO "+engine+"_threads(id,workspace_id,title) VALUES('same-thread','workspace','Migration')"); err != nil {
			t.Fatal(err)
		}
		id := "task-" + engine
		if conflict && engine == "gemini" {
			id = "task-codex"
		}
		var last any = at.Add(-time.Hour)
		if engine == "codex" {
			last = nil
		}
		_, err := db.ExecContext(ctx, s.Q("INSERT INTO "+engine+"_scheduled_tasks("+columns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"), id, "same-thread", "server", "Name "+engine, "Prompt", "@daily", "Asia/Shanghai", index%2, "native-model", "high", "default", "high", at.Add(time.Duration(index)*time.Hour), last, "succeeded", "retained message", "operation-"+engine, at.Add(-24*time.Hour), at)
		if err != nil {
			t.Fatal(err)
		}
	}
	if conflict {
		_, err := runMigrations(ctx, db, driver, schemaMigrations)
		if err == nil || !strings.Contains(err.Error(), "duplicate scheduled task IDs: task-codex") {
			t.Fatalf("conflict not identified: %v", err)
		}
		var count int
		if err := db.Get(&count, "SELECT COUNT(*) FROM schema_migrations WHERE version>=2"); err != nil || count != 0 {
			t.Fatalf("failed migration recorded: %d %v", count, err)
		}
		query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='scheduled_tasks'"
		if driver == "pgx" {
			query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='scheduled_tasks'"
		}
		if err := db.Get(&count, query); err != nil || count != 0 {
			t.Fatalf("failed migration left table: %d %v", count, err)
		}
		if _, err := db.Exec("UPDATE gemini_scheduled_tasks SET id='task-gemini' WHERE id='task-codex'"); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := runMigrations(ctx, db, driver, schemaMigrations); err != nil {
			t.Fatal(err)
		}
	}
	for _, engine := range []string{"codex", "gemini", "claude"} {
		var differences int
		query := "SELECT COUNT(*) FROM (SELECT " + columns + " FROM " + engine + "_scheduled_tasks EXCEPT SELECT " + columns + " FROM scheduled_tasks WHERE engine=?) difference"
		if err := db.Get(&differences, s.Q(query), engine); err != nil || differences != 0 {
			t.Fatalf("%s migration differences=%d: %v", engine, differences, err)
		}
		task, err := s.ScheduledTask(ctx, "task-"+engine)
		if err != nil || task.AgentKind != engine || task.Name != "Name "+engine {
			t.Fatalf("%s projected task: %+v %v", engine, task, err)
		}
	}
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM scheduled_tasks"); err != nil || count != 3 {
		t.Fatalf("migrated count=%d: %v", count, err)
	}
	if err := db.Get(&count, "SELECT COUNT(*) FROM schema_migrations WHERE version=2"); err != nil || count != 1 {
		t.Fatalf("history count=%d: %v", count, err)
	}
	if err := s.MarkScheduledTaskRun(ctx, "task-gemini", "running", "new result", "new-operation", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Get(&status, "SELECT last_run_status FROM gemini_scheduled_tasks WHERE id='task-gemini'"); err != nil || status != "succeeded" {
		t.Fatalf("legacy table changed: %s %v", status, err)
	}
	if err := s.DeleteClaudeThread(ctx, "same-thread"); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&count, "SELECT COUNT(*) FROM scheduled_tasks WHERE thread_id='same-thread'"); err != nil || count != 2 {
		t.Fatalf("namespace cleanup count=%d: %v", count, err)
	}
	if err := db.Get(&count, "SELECT COUNT(*) FROM scheduled_tasks WHERE engine='claude'"); err != nil || count != 0 {
		t.Fatalf("orphan task count=%d: %v", count, err)
	}
	if driver == "pgx" {
		db.SetMaxOpenConns(3)
	}
	assertScheduledTaskDeletionRace(t, s)

}

func assertScheduledTaskDeletionRace(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, engine := range []string{"codex", "gemini", "claude"} {
		for range 8 {
			id := NewID()
			if _, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO "+engine+"_threads(id,workspace_id,title) VALUES(?,'workspace','race')"), id); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			created := make(chan error, 1)
			deleted := make(chan error, 1)
			go func() {
				<-start
				_, err := s.CreateScheduledTask(ctx, ScheduledTaskInput{AgentKind: engine, ThreadID: id, Name: "race", Prompt: "race", Schedule: "@daily", Timezone: "UTC", Enabled: true}, time.Now().UTC())
				created <- err
			}()
			go func() {
				<-start
				var err error
				switch engine {
				case "codex":
					err = s.DeleteThread(ctx, id)
				case "gemini":
					err = s.DeleteGeminiThread(ctx, id)
				case "claude":
					err = s.DeleteClaudeThread(ctx, id)
				}
				deleted <- err
			}()
			close(start)
			createErr, deleteErr := <-created, <-deleted
			if createErr != nil && !errors.Is(createErr, sql.ErrNoRows) {
				t.Fatalf("%s concurrent create: %v", engine, createErr)
			}
			if deleteErr != nil {
				t.Fatalf("%s concurrent delete: %v", engine, deleteErr)
			}
			var count int
			if err := s.DB.Get(&count, s.Q("SELECT COUNT(*) FROM scheduled_tasks WHERE engine=? AND thread_id=?"), engine, id); err != nil || count != 0 {
				t.Fatalf("%s orphan tasks=%d: %v", engine, count, err)
			}
		}
	}
}
