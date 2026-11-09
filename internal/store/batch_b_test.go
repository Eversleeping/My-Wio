package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func batchBFixture(t *testing.T, db *Store) Thread {
	t.Helper()
	ctx := context.Background()
	server, project, workspace := NewID(), NewID(), NewID()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO servers(id,name,last_seen_at,agent_version) VALUES(?,?,?,?)", []any{server, "Batch B", time.Now().UTC(), "0.2.99"}},
		{"INSERT INTO projects(id,name) VALUES(?,?)", []any{project, project}},
		{"INSERT INTO workspaces(id,server_id,project_id,path) VALUES(?,?,?,?)", []any{workspace, server, project, "/srv/" + workspace}},
	} {
		if _, err := db.DB.ExecContext(ctx, db.Q(statement.query), statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	thread, err := db.CreateThread(ctx, workspace, "Batch B")
	if err != nil {
		t.Fatal(err)
	}
	return thread
}

func TestRetentionDefaultsAndOptIn(t *testing.T) { assertRetentionDefaultsAndOptIn(t, testStore(t)) }
func assertRetentionDefaultsAndOptIn(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	thread := batchBFixture(t, db)
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-8 * 24 * time.Hour)
	user := NewID()
	if err := db.CreateUser(ctx, User{ID: user, Username: user, AuthMode: "password"}); err != nil {
		t.Fatal(err)
	}
	for _, expires := range []time.Time{now, now.Add(time.Hour)} {
		if err := db.CreateSession(ctx, user, NewID(), "csrf", expires); err != nil {
			t.Fatal(err)
		}
	}
	var events []protocol.StreamEvent
	for _, item := range []struct {
		stream, kind string
		at           time.Time
	}{
		{thread.ServerID, "operation.succeeded", old},
		{thread.ServerID, "agent.updated", old},
		{thread.ServerID, "operation.failed", old}, // last sequence must survive
		{thread.ID, "operation.failed", old},       // conversation stream must survive
		{"orphan-" + thread.ID, "operation.failed", old},
		{thread.ID, "user.message", old},
	} {
		event, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: item.stream, Kind: item.kind, OccurredAt: item.at, Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	for _, bucket := range []time.Time{old, now.Add(-7 * 24 * time.Hour), now} {
		_, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO metric_rollups(server_id,bucket_at,cpu_percent,memory_percent,disk_percent,load_1,net_rx_bytes,net_tx_bytes) VALUES(?,?,0,0,0,0,0,0)"), thread.ServerID, bucket)
		if err != nil {
			t.Fatal(err)
		}
	}
	audit := NewID()
	if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO audit_log(id,action,resource_type,occurred_at) VALUES(?,'test','test',?)"), audit, old); err != nil {
		t.Fatal(err)
	}
	var operations []string
	for _, kind := range []string{"inventory.scan", "git.fetch", "codex.turn.start"} {
		id, err := db.QueueOperation(ctx, thread.ServerID, kind, map[string]string{}, NewID())
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, id)
	}
	for _, id := range []string{operations[0], operations[2]} {
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET status='succeeded',completed_at=? WHERE id=?"), old, id); err != nil {
			t.Fatal(err)
		}
	}
	checkCount := func(query string, want int, args ...any) {
		t.Helper()
		var got int
		if err := db.DB.GetContext(ctx, &got, db.Q(query), args...); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d: %v", query, got, want, err)
		}
	}
	if err := db.PruneRetainedData(ctx, now, RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	checkCount("SELECT COUNT(*) FROM sessions WHERE user_id=?", 1, user)
	checkCount("SELECT COUNT(*) FROM metric_rollups WHERE server_id=?", 2, thread.ServerID)
	for i, event := range events {
		want := 1
		if i == 0 {
			want = 0
		}
		checkCount("SELECT COUNT(*) FROM events WHERE event_id=?", want, event.EventID)
	}
	checkCount("SELECT COUNT(*) FROM audit_log WHERE id=?", 1, audit)
	checkCount("SELECT COUNT(*) FROM agent_operations WHERE server_id=?", 3, thread.ServerID)
	if err := db.PruneRetainedData(ctx, now, RetentionPolicy{AuditLog: 7 * 24 * time.Hour, OperationHistory: 7 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	checkCount("SELECT COUNT(*) FROM audit_log WHERE id=?", 0, audit)
	checkCount("SELECT COUNT(*) FROM agent_operations WHERE id=?", 0, operations[0])
	checkCount("SELECT COUNT(*) FROM agent_operations WHERE id=?", 1, operations[1]) // active
	checkCount("SELECT COUNT(*) FROM agent_operations WHERE id=?", 1, operations[2]) // native history
	next, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ServerID, Kind: "operation.succeeded", Payload: json.RawMessage(`{}`)})
	if err != nil || next.Sequence != 4 {
		t.Fatalf("server sequence reset: %#v %v", next, err)
	}
	if err := db.PruneRetainedData(ctx, now, RetentionPolicy{AuditLog: -1}); err == nil {
		t.Fatal("negative retention accepted")
	}
}

func TestCodexRequestsReplayAndConflict(t *testing.T) {
	assertCodexRequestsReplayAndConflict(t, testStore(t))
}
func assertCodexRequestsReplayAndConflict(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	thread := batchBFixture(t, db)
	command := protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "hello", ApprovalMode: "on-request"}
	type result struct {
		id    string
		event protocol.StreamEvent
		err   error
	}
	outcomes := make(chan result, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, event, err := db.QueueCodexTurnRequest(ctx, thread, command, "turn-request")
			outcomes <- result{id, event, err}
		}()
	}
	wg.Wait()
	close(outcomes)
	operationID, messages := "", 0
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if operationID == "" {
			operationID = outcome.id
		}
		if operationID != outcome.id {
			t.Fatal("duplicate turn operation")
		}
		if outcome.event.EventID != "" {
			messages++
		}
	}
	if messages != 1 {
		t.Fatalf("created %d user messages", messages)
	}
	command.Prompt = "different"
	if _, _, err := db.QueueCodexTurnRequest(ctx, thread, command, "turn-request"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed request: %v", err)
	}
	command.Prompt = "hello"
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET codex_thread_id='native',status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "turn.accepted", Payload: json.RawMessage(`{"turn_id":"turn"}`)}); err != nil {
		t.Fatal(err)
	}
	steer := protocol.SteerTurnCommand{ThreadID: thread.ID, CodexThread: "native", ExpectedTurnID: "turn", Prompt: "correction"}
	id, event, err := db.QueueCodexSteerRequest(ctx, thread, steer, "steer-request")
	if err != nil || event.EventID == "" {
		t.Fatalf("steer: %s %#v %v", id, event, err)
	}
	if err := db.SetThreadStatus(ctx, thread.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	replay, replayEvent, err := db.QueueCodexSteerRequest(ctx, thread, steer, "steer-request")
	if err != nil || replay != id || replayEvent.EventID != "" {
		t.Fatalf("finished steer replay: %s %#v %v", replay, replayEvent, err)
	}
	replay, replayEvent, err = db.QueueCodexTurnRequest(ctx, thread, command, "turn-request")
	if err != nil || replay != operationID || replayEvent.EventID != "" {
		t.Fatalf("finished turn replay: %s %#v %v", replay, replayEvent, err)
	}
	steer.Prompt = "changed"
	if _, _, err := db.QueueCodexSteerRequest(ctx, thread, steer, "steer-request"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("steer conflict: %v", err)
	}
}

func TestCodexSteerRollsBackOperationWhenEventInsertFails(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	thread := batchBFixture(t, db)
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET codex_thread_id='native',status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "turn.accepted", Payload: json.RawMessage(`{"turn_id":"turn"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, `CREATE TRIGGER fail_steer_event BEFORE INSERT ON events WHEN NEW.kind='user.message' BEGIN SELECT RAISE(ABORT,'event unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	command := protocol.SteerTurnCommand{ThreadID: thread.ID, CodexThread: "native", ExpectedTurnID: "turn", Prompt: "correction"}
	if _, _, err := db.QueueCodexSteerRequest(ctx, thread, command, "request"); err == nil {
		t.Fatal("event failure ignored")
	}
	var n int
	for _, table := range []string{"codex_requests", "agent_operations"} {
		if err := db.DB.GetContext(ctx, &n, db.Q("SELECT COUNT(*) FROM "+table+" WHERE thread_id=?"), thread.ID); err != nil || n != 0 {
			t.Fatalf("partial %s write: %d %v", table, n, err)
		}
	}
}

func TestWorkspacePathFilterMatchesLiteralWildcards(t *testing.T) {
	assertWorkspacePathFilterMatchesLiteralWildcards(t, testStore(t))
}
func assertWorkspacePathFilterMatchesLiteralWildcards(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	thread := batchBFixture(t, db)
	for i, path := range []string{`/srv/100%_done\repo`, `/srv/100XYdone/repo`, `/srv/plain`} {
		if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO workspaces(id,server_id,project_id,path) VALUES(?,?,?,?)"), fmt.Sprintf("%s-%d", thread.WorkspaceID, i), thread.ServerID, thread.ProjectID, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"%", "_", `\`, `100%_done\repo`} {
		rows, err := db.ListWorkspacesFiltered(ctx, query, thread.ServerID, "")
		if err != nil || len(rows) != 1 || rows[0].Path != `/srv/100%_done\repo` {
			t.Fatalf("literal %q: %#v %v", query, rows, err)
		}
	}
}

func TestRetentionMigrationIsRepeatableAndBackfillsImports(t *testing.T) {
	assertRetentionMigrationIsRepeatableAndBackfillsImports(t, testStore(t))
}

func TestQueueOperationAssociatesProjectImports(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	thread := batchBFixture(t, db)
	id, err := db.QueueOperation(ctx, thread.ServerID, "git.import", protocol.GitImportCommand{ProjectID: thread.ProjectID}, NewID())
	if err != nil {
		t.Fatal(err)
	}
	op, err := db.LatestProjectImport(ctx, thread.ProjectID)
	if err != nil || op.ID != id || op.Command.ProjectID != thread.ProjectID {
		t.Fatalf("new import association: %#v %v", op, err)
	}
	if active, err := db.HasActiveProjectImport(ctx, thread.ProjectID); err != nil || !active {
		t.Fatalf("new active import: %v %v", active, err)
	}
}

func assertRetentionMigrationIsRepeatableAndBackfillsImports(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	thread := batchBFixture(t, db)
	raw, _ := json.Marshal(protocol.GitImportCommand{ProjectID: thread.ProjectID})
	// Seed the old NULL metadata explicitly: current writers must populate it.
	id := NewID()
	if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,'git.import',?,?,?)"), id, thread.ServerID, string(raw), NewID(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateRetentionAndCodexRequests(ctx, db.DB); err != nil {
			t.Fatal(err)
		}
	}
	op, err := db.LatestProjectImport(ctx, thread.ProjectID)
	if err != nil || op.ID != id {
		t.Fatalf("legacy import: %#v %v", op, err)
	}
	if active, err := db.HasActiveProjectImport(ctx, thread.ProjectID); err != nil || !active {
		t.Fatalf("legacy active: %v %v", active, err)
	}
	// A failed historical row can coexist with the still-active import. Do not
	// briefly enqueue a second active row, which the unique index forbids.
	newer := NewID()
	if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO agent_operations(id,server_id,project_id,kind,payload,status,idempotency_key,created_at) VALUES(?,?,?,'git.import',?,'failed',?,?)"), newer, thread.ServerID, thread.ProjectID, string(raw), NewID(), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.listProjectImports(ctx, thread.ProjectID)
	if err != nil || len(rows) != 1 || rows[0].ID != newer {
		t.Fatalf("historical import scan: %#v %v", rows, err)
	}
	if active, err := db.HasActiveProjectImport(ctx, thread.ProjectID); err != nil || !active {
		t.Fatalf("active hidden by later failure: %v %v", active, err)
	}
}
