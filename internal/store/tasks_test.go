package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/task"
)

type taskFixture struct {
	db                         *Store
	project, workspace, server string
}

func newTaskFixture(t *testing.T, db *Store) taskFixture {
	t.Helper()
	f := taskFixture{db: db, project: NewID(), workspace: NewID(), server: NewID()}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO servers(id,name) VALUES(?,?)", []any{f.server, "Task agent"}},
		{"INSERT INTO projects(id,name) VALUES(?,?)", []any{f.project, "Task project"}},
		{"INSERT INTO workspaces(id,project_id,server_id,path,status,management_mode,kind) VALUES(?,?,?,?,'ready','managed','worktree')", []any{f.workspace, f.project, f.server, "/srv/task-" + f.workspace}},
	} {
		if _, err := db.DB.Exec(db.Q(q.sql), q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f taskFixture) create(t *testing.T, key string) task.Task {
	t.Helper()
	result, err := f.db.CreateTask(context.Background(), task.Create{Meta: task.Meta{Actor: "task-user", RequestID: key}, ProjectID: f.project, Title: "Task", Requirement: "Build a feature", Criteria: []task.Criterion{{ID: "works", Text: "Feature works"}}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f taskFixture) plan(version int64, key string) task.AppendPlan {
	return task.AppendPlan{Meta: task.Meta{Actor: "task-user", RequestID: key, ExpectedVersion: version}, Reason: "confirmed explicit plan", Members: []task.Member{
		{ID: "dev", Role: "developer", Engine: "codex", WorkspaceID: f.workspace, Mode: "on-request", DirectoryMode: "existing_worktree"},
		{ID: "research", Role: "researcher", Engine: "gemini", WorkspaceID: f.workspace, Model: "auto", Mode: "default", DirectoryMode: "shared"},
		{ID: "review", Role: "reviewer", Engine: "claude", WorkspaceID: f.workspace, Model: "sonnet", Mode: "default", DirectoryMode: "shared"},
	}, Steps: []task.Step{
		{ID: "a", MemberID: "dev", Kind: "develop", Title: "Develop", Instructions: "Build feature", Required: true, CompletionPolicy: "execution", CriterionIDs: []string{"works"}},
		{ID: "b", MemberID: "research", Kind: "research", Title: "Research", Instructions: "Research approach", Required: true, CompletionPolicy: "execution"},
		{ID: "c", MemberID: "review", Kind: "summarize", Title: "Summarize", Instructions: "Summarize results", Required: true, CompletionPolicy: "execution", DependsOn: []string{"a", "b"}, Inputs: []task.Input{{Kind: "step_result", StepID: "a"}}},
	}}
}
func taskCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *task.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}
func taskCount(t *testing.T, db *Store, table string) int {
	t.Helper()
	var n int
	if err := db.DB.Get(&n, "SELECT COUNT(*) FROM "+table); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTaskCoreStorage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tasks.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertTaskCoreStorage(t, db)
}

// Shared with PostgreSQL so the API contract's transactional guarantees are
// exercised against real row locks, foreign keys and uniqueness constraints.
func assertTaskCoreStorage(t *testing.T, db *Store) {
	ctx := context.Background()
	f := newTaskFixture(t, db)
	created := f.create(t, "create")
	in := task.Create{Meta: task.Meta{Actor: "task-user", RequestID: "create"}, ProjectID: f.project, Title: "Task", Requirement: "Build a feature", Criteria: created.Criteria}
	replay, err := db.CreateTask(ctx, in)
	if err != nil || !reflect.DeepEqual(created, replay) {
		t.Fatalf("create replay: %+v %v", replay, err)
	}
	in.Title = "changed"
	_, err = db.CreateTask(ctx, in)
	taskCode(t, err, task.IdempotencyConflict)
	plan := f.plan(created.Version, "plan")
	p, err := db.AppendTaskPlan(ctx, f.project, created.ID, plan)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := db.AppendTaskPlan(ctx, f.project, created.ID, plan)
	if err != nil || !reflect.DeepEqual(p, p2) {
		t.Fatalf("plan replay: %v", err)
	}
	if p.Plan.Revision != 1 || p.TaskVersion != 2 || len(p.Plan.ContentHash) != 64 {
		t.Fatal(p)
	}
	detail, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || detail.LastSequence != 2 || detail.Run != nil || detail.Task.State != "draft" || detail.Capabilities["start_run"] {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	if !reflect.DeepEqual(detail.Plan, &p.Plan) {
		t.Fatal("plan snapshot changed")
	}
	var members, steps, deps int
	for _, q := range []struct {
		out   *int
		query string
	}{{&members, "SELECT COUNT(*) FROM task_plan_members WHERE plan_id=?"}, {&steps, "SELECT COUNT(*) FROM task_plan_steps WHERE plan_id=?"}, {&deps, "SELECT COUNT(*) FROM task_step_dependencies WHERE plan_id=?"}} {
		if err := db.DB.Get(q.out, db.Q(q.query), p.Plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	if members != 3 || steps != 3 || deps != 2 {
		t.Fatalf("normalized plan: %d %d %d", members, steps, deps)
	}
	text := "New requirement"
	updated, err := db.UpdateTask(ctx, f.project, created.ID, task.Update{Meta: task.Meta{Actor: "task-user", RequestID: "update", ExpectedVersion: 2}, Requirement: &text})
	if err != nil || updated.SpecVersion != 2 || updated.Version != 3 {
		t.Fatalf("update %+v %v", updated, err)
	}
	old, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil || old.Plan.Requirement != created.Requirement || old.Plan.SpecVersion != 1 {
		t.Fatal("immutable requirement snapshot was rewritten", err)
	}
	_, err = db.UpdateTask(ctx, f.project, created.ID, task.Update{Meta: task.Meta{Actor: "task-user", RequestID: "stale", ExpectedVersion: 2}, Requirement: &text})
	taskCode(t, err, task.Conflict)
	foreign := newTaskFixture(t, db)
	if _, err := db.TaskDetail(ctx, foreign.project, created.ID); !IsNotFound(err) {
		t.Fatalf("cross-project detail: %v", err)
	}
	if _, err := db.TaskEvents(ctx, foreign.project, created.ID, 0, 10); !IsNotFound(err) {
		t.Fatalf("cross-project events: %v", err)
	}
	bad := f.plan(3, "foreign")
	bad.ParentID = &p.Plan.ID
	bad.Members[0].WorkspaceID = foreign.workspace
	if _, err := db.AppendTaskPlan(ctx, f.project, created.ID, bad); !IsNotFound(err) {
		t.Fatalf("cross-project plan: %v", err)
	}
	if _, err := db.TaskRequest(ctx, "other-user", f.project, "create"); !IsNotFound(err) {
		t.Fatalf("request actor scope: %v", err)
	}
	if _, err := db.TaskRequest(ctx, "task-user", foreign.project, "plan"); !IsNotFound(err) {
		t.Fatalf("request project scope: %v", err)
	}
	events, err := db.TaskEvents(ctx, f.project, created.ID, 0, 1)
	if err != nil || !events.HasMore || events.NextAfter != 1 || events.Items[0].Kind != "task.created" {
		t.Fatalf("events %+v %v", events, err)
	}
	events, err = db.TaskEvents(ctx, f.project, created.ID, events.NextAfter, 10)
	if err != nil || events.HasMore || len(events.Items) != 2 || events.NextAfter != 3 {
		t.Fatalf("events %+v %v", events, err)
	}
	request, err := db.TaskRequest(ctx, "task-user", f.project, "plan")
	if err != nil || request.Status != 201 || request.TaskID != created.ID {
		t.Fatal(request, err)
	}
	assertTaskConcurrency(t, f, created.ID, updated.Version)
	assertTaskPagination(t, f)
	assertTaskRollback(t, f)
	assertTaskRetention(t, f, p.Plan.ID, created.ID)
}
func assertTaskConcurrency(t *testing.T, f taskFixture, id string, version int64) {
	t.Helper()
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			text := fmt.Sprint("concurrent-", i)
			_, err := f.db.UpdateTask(ctx, f.project, id, task.Update{Meta: task.Meta{Actor: "task-user", RequestID: fmt.Sprint("race-", i), ExpectedVersion: version}, Title: &text})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			taskCode(t, err, task.Conflict)
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("update winners %d conflicts %d", success, conflict)
	}
	// Two retries of the same creation have exactly the same identity and event.
	in := task.Create{Meta: task.Meta{Actor: "task-user", RequestID: "same-create"}, ProjectID: f.project, Title: "same", Requirement: "same", Criteria: []task.Criterion{{ID: "c", Text: "check"}}}
	ids := make(chan string, 2)
	start = make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := f.db.CreateTask(ctx, in)
			resultsErr := err
			if resultsErr != nil {
				ids <- "error: " + err.Error()
				return
			}
			ids <- result.ID
		}()
	}
	close(start)
	wg.Wait()
	a, b := <-ids, <-ids
	if a != b || len(a) != 36 {
		t.Fatalf("duplicate creates: %q %q", a, b)
	}
}
func assertTaskPagination(t *testing.T, f taskFixture) {
	t.Helper()
	ctx := context.Background()
	f.create(t, "page-one")
	f.create(t, "page-two")
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		page, err := f.db.TaskPage(ctx, task.Filter{ProjectID: f.project, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("pagination duplicated a task")
			}
			seen[item.ID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	var n int
	if err := f.db.DB.Get(&n, f.db.Q("SELECT COUNT(*) FROM tasks WHERE project_id=?"), f.project); err != nil || len(seen) != n {
		t.Fatalf("pagination %d/%d %v", len(seen), n, err)
	}
	if cursor == "" {
		t.Fatal("pagination fixture did not produce a cursor")
	}
	_, err := f.db.TaskPage(ctx, task.Filter{ProjectID: f.project, Limit: 1, State: "accepted", Cursor: cursor})
	taskCode(t, err, task.Invalid)
}
func injectTaskFailure(t *testing.T, db *Store, table string) func() {
	t.Helper()
	var queries []string
	condition := ""
	if table == "task_events" {
		table = "events"
		condition = "NEW.stream_id LIKE 'task:%'"
	}
	if db.isPostgres() {
		queries = []string{"CREATE FUNCTION task_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected task failure'; END $$", "CREATE TRIGGER task_test_failure BEFORE INSERT ON " + table + " FOR EACH ROW " + func() string {
			if condition != "" {
				return "WHEN (" + condition + ") "
			}
			return ""
		}() + "EXECUTE FUNCTION task_test_fail()"}
	} else {
		queries = []string{"CREATE TRIGGER task_test_failure BEFORE INSERT ON " + table + func() string {
			if condition != "" {
				return " WHEN " + condition
			}
			return ""
		}() + " BEGIN SELECT RAISE(ABORT,'injected task failure'); END"}
	}
	for _, q := range queries {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		q := "DROP TRIGGER task_test_failure"
		if db.isPostgres() {
			q += " ON " + table
		}
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
		if db.isPostgres() {
			if _, err := db.DB.Exec("DROP FUNCTION task_test_fail()"); err != nil {
				t.Fatal(err)
			}
		}
	}
}
func assertTaskRollback(t *testing.T, f taskFixture) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"tasks", "events", "task_requests"} {
		before := taskCount(t, f.db, "tasks")
		remove := injectTaskFailure(t, f.db, table)
		_, err := f.db.CreateTask(ctx, task.Create{Meta: task.Meta{Actor: "task-user", RequestID: "fail-create"}, ProjectID: f.project, Title: "fail", Requirement: "fail", Criteria: []task.Criterion{{ID: "c", Text: "check"}}})
		remove()
		if err == nil || taskCount(t, f.db, "tasks") != before {
			t.Fatalf("create not rolled back at %s: %v", table, err)
		}
	}
	created := f.create(t, "rollback-plan")
	for _, table := range []string{"task_plan_revisions", "task_plan_members", "task_plan_steps", "task_step_dependencies", "events", "task_requests"} {
		before := map[string]int{}
		for _, name := range []string{"task_plan_revisions", "task_plan_members", "task_plan_steps", "task_step_dependencies", "events", "task_requests"} {
			before[name] = taskCount(t, f.db, name)
		}
		remove := injectTaskFailure(t, f.db, table)
		_, err := f.db.AppendTaskPlan(ctx, f.project, created.ID, f.plan(1, "retry-same-plan"))
		remove()
		if err == nil {
			t.Fatalf("no failure at %s", table)
		}
		for name, n := range before {
			if taskCount(t, f.db, name) != n {
				t.Fatalf("partial write at %s into %s", table, name)
			}
		}
		detail, err := f.db.TaskDetail(ctx, f.project, created.ID)
		if err != nil || detail.Task.Version != 1 || detail.Task.Counters.Plans != 0 {
			t.Fatal("rollback consumed version or budget", err)
		}
	}
	if _, err := f.db.AppendTaskPlan(ctx, f.project, created.ID, f.plan(1, "retry-same-plan")); err != nil {
		t.Fatal("rollback consumed request id", err)
	}
}
func assertTaskRetention(t *testing.T, f taskFixture, planID, taskID string) {
	t.Helper()
	ctx := context.Background()
	projectPlan, err := f.db.ProjectDeletionPlan(ctx, f.project)
	if err != nil || projectPlan.CanDeleteMetadata || projectPlan.CanDeleteManagedFiles || projectPlan.TaskCount == 0 {
		t.Fatal("project task deletion guard", err)
	}
	if _, err := f.db.DeleteProjectMetadata(ctx, f.project); !errors.Is(err, ErrProjectDeletionBlocked) {
		t.Fatal("project deletion allowed", err)
	}
	workspacePlan, err := f.db.WorkspaceDeletionPlan(ctx, f.workspace, true)
	if err != nil || workspacePlan.CanDeleteFiles || workspacePlan.CanRemoveRecord {
		t.Fatal("force bypassed workspace retention", err)
	}
	if err := f.db.RevokeServer(ctx, f.server); !errors.Is(err, ErrStateConflict) {
		t.Fatal("server evidence deletion allowed", err)
	}
	w, err := f.db.Workspace(ctx, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueWorkspaceLifecycle(ctx, w, protocol.GitWorkspaceLifecycleCommand{Action: "delete", WorkspaceID: w.ID, ProjectID: w.ProjectID, SourcePath: w.Path, WorkspaceKind: w.Kind, Force: true}); !errors.Is(err, ErrStateConflict) {
		t.Fatal("queued deletion bypassed retained plan", err)
	}
	assertTaskAttemptConstraintsAndRetention(t, f, planID, taskID)
	if err := f.db.PruneRetainedData(ctx, time.Now().UTC().Add(365*24*time.Hour), RetentionPolicy{OperationHistory: time.Hour, AuditLog: time.Hour}); err != nil {
		t.Fatal(err)
	}
	detail, err := f.db.TaskDetail(ctx, f.project, taskID)
	if err != nil || detail.Plan.ID != planID || detail.LastSequence < 2 {
		t.Fatal("task evidence pruned", err)
	}
}

func assertTaskAttemptConstraintsAndRetention(t *testing.T, f taskFixture, planID, taskID string) {
	t.Helper()
	ctx := context.Background()
	db := f.db
	runID := NewID()
	now := time.Now().UTC()
	if _, err := db.DB.Exec(db.Q("INSERT INTO task_grant_revisions(task_id,revision,actor,grant_json,created_at) VALUES(?,1,'task-user','{}',?)"), taskID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(db.Q("INSERT INTO task_runs(id,task_id,plan_id,grant_revision,state) VALUES(?,?,?,1,'running')"), runID, taskID, planID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(db.Q("INSERT INTO task_runs(id,task_id,plan_id,grant_revision,state) VALUES(?,?,?,1,'running')"), NewID(), taskID, planID); err == nil {
		t.Fatal("multiple active runs admitted")
	}
	for i, engine := range []string{"codex", "gemini", "claude"} {
		step := []string{"a", "b", "c"}[i]
		if _, err := db.DB.Exec(db.Q("INSERT INTO task_step_runs(task_id,run_id,plan_id,step_id,state) VALUES(?,?,?,?,'active')"), taskID, runID, planID, step); err != nil {
			t.Fatal(err)
		}
		var sessionID string
		switch engine {
		case "codex":
			s, err := db.CreateThread(ctx, f.workspace, "task evidence")
			if err != nil {
				t.Fatal(err)
			}
			sessionID = s.ID
		case "gemini":
			s, err := db.CreateGeminiThread(ctx, f.workspace, "auto", "default")
			if err != nil {
				t.Fatal(err)
			}
			sessionID = s.ID
		case "claude":
			s, err := db.CreateClaudeThread(ctx, f.workspace, "sonnet", "default", "")
			if err != nil {
				t.Fatal(err)
			}
			sessionID = s.ID
		}
		op, err := db.QueueOperation(ctx, f.server, protocol.OpWorkspaceFiles, map[string]string{"workspace_id": f.workspace}, "task-probe:"+NewID())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(db.Q("UPDATE agent_operations SET status='succeeded',completed_at=? WHERE id=?"), now.Add(-365*24*time.Hour), op); err != nil {
			t.Fatal(err)
		}
		attemptID := NewID()
		key := "task:" + runID + ":" + step + ":1"
		query := "INSERT INTO task_attempts(id,task_id,run_id,plan_id,step_id,number,dispatch_key,input_hash,input_json,state,engine,session_id,server_id,workspace_id,source_json,operation_id,created_at) VALUES(?,?,?,?,?,1,?,'hash','{}','succeeded',?,?,?,?,'{}',?,?)"
		args := []any{attemptID, taskID, runID, planID, step, key, engine, sessionID, f.server, f.workspace, op, now}
		if _, err := db.DB.Exec(db.Q(query), args...); err != nil {
			t.Fatal(err)
		}
		duplicate := append([]any(nil), args...)
		duplicate[0] = NewID()
		if _, err := db.DB.Exec(db.Q(query), duplicate...); err == nil {
			t.Fatal("duplicate attempt admitted")
		}
		if _, err := db.DB.Exec(db.Q("INSERT INTO task_session_bindings(id,task_id,run_id,engine,session_id,active_attempt_id) VALUES(?,?,?,?,?,?)"), NewID(), taskID, runID, engine, sessionID, attemptID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(db.Q("INSERT INTO task_session_bindings(id,task_id,run_id,engine,session_id) VALUES(?,?,?,?,?)"), NewID(), taskID, runID, engine, sessionID); err == nil {
			t.Fatal("duplicate session ownership admitted")
		}
		event, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: sessionID, Kind: protocol.EventUserMessage, Payload: json.RawMessage(`{"text":"evidence"}`)})
		if err != nil {
			t.Fatal(err)
		}
		var deleteErr error
		switch engine {
		case "codex":
			deleteErr = db.DeleteThread(ctx, sessionID)
		case "gemini":
			deleteErr = db.DeleteGeminiThread(ctx, sessionID)
		case "claude":
			deleteErr = db.DeleteClaudeThread(ctx, sessionID)
		}
		if !errors.Is(deleteErr, ErrStateConflict) {
			t.Fatalf("%s task session deleted: %v", engine, deleteErr)
		}
		if err := db.PruneRetainedData(ctx, now.Add(365*24*time.Hour), RetentionPolicy{OperationHistory: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Operation(ctx, op); err != nil {
			t.Fatal("task operation pruned", err)
		}
		var count int
		if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM events WHERE event_id=?"), event.EventID); err != nil || count != 1 {
			t.Fatal("session evidence lost", err)
		}
	}
	foreign := newTaskFixture(t, db)
	other := foreign.create(t, "foreign-constraint")
	if _, err := db.DB.Exec(db.Q("INSERT INTO task_runs(id,task_id,plan_id,grant_revision,state) VALUES(?,?,?,1,'running')"), NewID(), other.ID, planID); err == nil {
		t.Fatal("cross-task plan/run admitted")
	}
	if _, err := db.DB.Exec(db.Q("INSERT INTO task_step_dependencies(plan_id,step_id,depends_on) VALUES(?,?,'missing')"), planID, "a"); err == nil {
		t.Fatal("unknown dependency admitted")
	}
	if _, err := db.DB.Exec(db.Q("UPDATE tasks SET current_run_id=? WHERE id=?"), runID, taskID); err != nil {
		t.Fatal(err)
	}
	detail, err := db.TaskDetail(ctx, f.project, taskID)
	if err != nil || detail.Task.State != "active" || len(detail.Steps) != 3 {
		t.Fatal("persisted run snapshot is incomplete", err)
	}
	if _, err := db.DB.Exec(db.Q("UPDATE task_runs SET state='accepted' WHERE id=?"), runID); err != nil {
		t.Fatal(err)
	}
	requirement := "A new requirement after acceptance"
	changed, err := db.UpdateTask(ctx, f.project, taskID, task.Update{Meta: task.Meta{Actor: "task-user", RequestID: "reopen-spec", ExpectedVersion: detail.Task.Version}, Requirement: &requirement})
	if err != nil || changed.State != "draft" || changed.CurrentRunID != nil {
		t.Fatal("old acceptance leaked into a new specification", err)
	}
	var state string
	if err := db.DB.Get(&state, db.Q("SELECT state FROM task_runs WHERE id=?"), runID); err != nil || state != "accepted" {
		t.Fatal("specification edit rewrote historical acceptance", err)
	}
}

func TestTaskCoreMigration(t *testing.T) {
	db, err := sqlx.Connect("sqlite", filepath.Join(t.TempDir(), "migration.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	assertTaskCoreMigration(t, db, "sqlite")
}
func assertTaskCoreMigration(t *testing.T, db *sqlx.DB, driver string) {
	t.Helper()
	ctx := context.Background()
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:6]); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db, driver: driver}
	f := newTaskFixture(t, s)
	refs := map[string]string{}
	for _, engine := range []string{"codex", "gemini", "claude"} {
		id := NewID()
		column := ""
		if engine == "codex" {
			column = ",title"
		}
		query := "INSERT INTO " + engine + "_threads(id,workspace_id" + column + ") VALUES(?,?"
		args := []any{id, f.workspace}
		if engine == "codex" {
			query += ",?"
			args = append(args, "native")
		}
		query += ")"
		if _, err := db.Exec(s.Q(query), args...); err != nil {
			t.Fatal(err)
		}
		refs[engine] = id
	}
	failure := errors.New("injected task migration failure")
	failed := append(append([]migration(nil), schemaMigrations[:6]...), migration{version: 7, name: "task_core", up: func(ctx context.Context, tx *sqlx.Tx, driver string) error {
		if err := migrateTaskCore(ctx, tx, driver); err != nil {
			return err
		}
		return failure
	}})
	if _, err := runMigrations(ctx, db, driver, failed); !errors.Is(err, failure) {
		t.Fatal("missing migration failure", err)
	}
	var tableCount int
	query := "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'task%'"
	if driver == "pgx" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name LIKE 'task%'"
	}
	if err := db.Get(&tableCount, query); err != nil || tableCount != 0 {
		t.Fatal("failed migration retained partial tables", err)
	}
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:7]); err != nil {
		t.Fatal(err)
	}
	failedExecution := append(append([]migration(nil), schemaMigrations[:7]...), migration{version: 8, name: "task_execution", up: func(ctx context.Context, tx *sqlx.Tx, driver string) error {
		if err := migrateTaskExecution(ctx, tx, driver); err != nil {
			return err
		}
		return failure
	}})
	if _, err := runMigrations(ctx, db, driver, failedExecution); !errors.Is(err, failure) {
		t.Fatal("missing execution migration failure", err)
	}
	var columns int
	columnQuery := "SELECT COUNT(*) FROM pragma_table_info('task_attempts') WHERE name='member_id'"
	if driver == "pgx" {
		columnQuery = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='task_attempts' AND column_name='member_id'"
	}
	if err := db.Get(&columns, columnQuery); err != nil || columns != 0 {
		t.Fatal("execution migration retained partial column", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := runMigrations(ctx, db, driver, schemaMigrations); err != nil {
			t.Fatal(err)
		}
	}
	for engine, id := range refs {
		var workspace string
		if err := db.Get(&workspace, s.Q("SELECT workspace_id FROM "+engine+"_threads WHERE id=?"), id); err != nil || workspace != f.workspace {
			t.Fatal("migration changed native session", err)
		}
	}
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:6]); err == nil {
		t.Fatal("old schema6 control plane accepted schema7")
	}
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:7]); err == nil {
		t.Fatal("old schema7 control plane accepted schema8")
	}
	var versions int
	if err := db.Get(&versions, "SELECT COUNT(*) FROM schema_migrations WHERE version=7"); err != nil || versions != 1 {
		t.Fatal("migration did not apply once", err)
	}
	created := f.create(t, "migration-create")
	if _, err := s.AppendTaskPlan(ctx, f.project, created.ID, f.plan(1, "migration-plan")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(s.Q("DELETE FROM projects WHERE id=?"), f.project); err == nil {
		t.Fatal("task project FK allowed cascade")
	}
	if _, err := db.Exec(s.Q("DELETE FROM workspaces WHERE id=?"), f.workspace); err == nil {
		t.Fatal("task workspace FK allowed cascade")
	}
	var events []string
	if err := db.Select(&events, s.Q("SELECT payload FROM events WHERE stream_id=? ORDER BY sequence"), "task:"+created.ID); err != nil {
		t.Fatal(err)
	}
	for _, raw := range events {
		var e task.Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil || e.SchemaVersion != 1 {
			t.Fatal("invalid transaction event", err)
		}
	}
}
