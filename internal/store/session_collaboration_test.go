package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type collaborationFixture struct {
	db                *Store
	server, workspace string
	source            sessiontools.Ref
	operation         string
}

func newCollaborationFixture(t *testing.T, db *Store, engine string) collaborationFixture {
	t.Helper()
	ctx := context.Background()
	server, project, workspace := NewID(), NewID(), NewID()
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO servers(id,name,agent_version,last_seen_at) VALUES(?,?,?,?)", []any{server, "Collaboration", buildinfo.Version, time.Now().UTC()}},
		{"INSERT INTO projects(id,name,default_branch) VALUES(?,?,'main')", []any{project, "Collaboration"}},
		{"INSERT INTO workspaces(id,project_id,server_id,path) VALUES(?,?,?,?)", []any{workspace, project, server, "/srv/collaboration-" + workspace}},
	} {
		if _, err := db.DB.ExecContext(ctx, db.Q(query.sql), query.args...); err != nil {
			t.Fatal(err)
		}
	}
	var ref sessiontools.Ref
	var op string
	switch engine {
	case "codex":
		thread, err := db.CreateThread(ctx, workspace, "Source")
		if err != nil {
			t.Fatal(err)
		}
		op, _, err = db.QueueCodexTurn(ctx, thread, protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: workspace, Workspace: thread.Path, Prompt: "task", ApprovalMode: "on-request"})
		if err != nil {
			t.Fatal(err)
		}
		ref = sessiontools.Ref{Engine: engine, ID: thread.ID}
	case "claude":
		thread, err := db.CreateClaudeThread(ctx, workspace, "sonnet", "default", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.QueueClaudeTurn(ctx, thread, "task", thread.Model, thread.PermissionMode, thread.Effort); err != nil {
			t.Fatal(err)
		}
		thread, err = db.ClaudeThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		op = thread.OperationID
		ref = sessiontools.Ref{Engine: engine, ID: thread.ID}
	case "gemini":
		thread, err := db.CreateGeminiThread(ctx, workspace, "auto", "default")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.QueueGeminiTurn(ctx, thread, "task", thread.Model, thread.ApprovalMode); err != nil {
			t.Fatal(err)
		}
		thread, err = db.GeminiThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		op = thread.OperationID
		ref = sessiontools.Ref{Engine: engine, ID: thread.ID}
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE "+engine+"_threads SET status='running' WHERE id=?"), ref.ID); err != nil {
		t.Fatal(err)
	}
	grant := sessiontools.Grant{Enabled: true, Worktree: true, TargetModes: map[string]string{"codex": "on-request", "claude": "default", "gemini": "default"}}
	if err := db.SetCollaborationGrant(ctx, ref, grant); err != nil {
		t.Fatal(err)
	}
	return collaborationFixture{db, server, workspace, ref, op}
}

func (f collaborationFixture) call(t *testing.T, name string, params sessiontools.Params) sessiontools.Reply {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return f.db.ExecuteCollaboration(context.Background(), f.server, sessiontools.Call{Source: f.source, OperationID: f.operation, Name: name, Arguments: raw})
}

func TestCollaborationEngineMatrixAndReplay(t *testing.T) {
	assertCollaborationEngineMatrixAndReplay(t, testStore(t))
}
func assertCollaborationEngineMatrixAndReplay(t *testing.T, db *Store) {
	for _, source := range []string{"codex", "claude", "gemini"} {
		for _, target := range []string{"codex", "claude", "gemini"} {
			t.Run(source+"-"+target, func(t *testing.T) {
				f := newCollaborationFixture(t, db, source)
				p := sessiontools.Params{RequestID: "empty", Engine: target}
				result := f.call(t, "wio_create_session", p)
				if result.Error != nil || result.Session == nil || result.Status != "succeeded" {
					t.Fatalf("create: %+v", result)
				}
				var count int
				if err := db.DB.GetContext(context.Background(), &count, db.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=?"), f.server); err != nil || count != 1 {
					t.Fatalf("empty creation called model: %d %v", count, err)
				}
				replay := f.call(t, "wio_create_session", p)
				if replay.Session == nil || *replay.Session != *result.Session {
					t.Fatalf("replay differs: %+v", replay)
				}
				p.Prompt = "changed"
				conflict := f.call(t, "wio_create_session", p)
				if conflict.Error == nil || conflict.Error.Code != "IDEMPOTENCY_CONFLICT" {
					t.Fatalf("conflict: %+v", conflict)
				}
				// Route tests isolate execution from the independently tested shared-workspace reservation.
				if _, err := db.DB.ExecContext(context.Background(), db.Q("UPDATE agent_operations SET workspace_write=0 WHERE id=?"), f.operation); err != nil {
					t.Fatal(err)
				}
				p = sessiontools.Params{RequestID: "send", Session: *result.Session, Prompt: "Run the tests"}
				sent := f.call(t, "wio_send_message", p)
				if sent.Error != nil || sent.OperationID == "" {
					t.Fatalf("send: %+v", sent)
				}
				sentAgain := f.call(t, "wio_send_message", p)
				if sentAgain.OperationID != sent.OperationID || sentAgain.Error != nil {
					t.Fatalf("send replay: %+v", sentAgain)
				}
				if err := db.DB.GetContext(context.Background(), &count, db.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=?"), f.server); err != nil || count != 2 {
					t.Fatalf("duplicate task: %d %v", count, err)
				}
				read := f.call(t, "wio_get_result", sessiontools.Params{Session: *result.Session})
				if read.Error != nil {
					t.Fatalf("read result: %+v", read)
				}
			})
		}
	}
}

func TestCollaborationAuthorizationAndRevocation(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	other, err := f.db.CreateThread(ctx, f.workspace, "Unrelated")
	if err != nil {
		t.Fatal(err)
	}
	ref := sessiontools.Ref{Engine: "codex", ID: other.ID}
	for _, tool := range []string{"wio_get_session", "wio_read_messages", "wio_send_message", "wio_interrupt_session", "wio_archive_session"} {
		reply := f.call(t, tool, sessiontools.Params{RequestID: tool, Session: ref, Prompt: "test"})
		if reply.Error == nil {
			t.Fatalf("unguarded %s: %+v", tool, reply)
		}
	}
	grant, _ := f.db.CollaborationGrant(ctx, f.source)
	grant.TargetModes = nil
	if err = f.db.SetCollaborationGrant(ctx, f.source, grant); err != nil {
		t.Fatal(err)
	}
	reply := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "mapping", Engine: "claude"})
	if reply.Error == nil || reply.Error.Code != "PERMISSION_MAPPING_REQUIRED" {
		t.Fatalf("permission mapping: %+v", reply)
	}
	grant.Enabled = false
	if err = f.db.SetCollaborationGrant(ctx, f.source, grant); err != nil {
		t.Fatal(err)
	}
	reply = f.call(t, "wio_create_session", sessiontools.Params{RequestID: "revoked"})
	if reply.Error == nil || reply.Error.Code != "OUT_OF_SCOPE" {
		t.Fatalf("revocation: %+v", reply)
	}
	raw := json.RawMessage(`{}`)
	reply = f.db.ExecuteCollaboration(ctx, "other-server", sessiontools.Call{Source: f.source, OperationID: f.operation, Name: "wio_get_context", Arguments: raw})
	if reply.Error == nil {
		t.Fatal("wrong server accepted")
	}
}

func TestCollaborationConcurrentCreateIsAtomic(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	var wg sync.WaitGroup
	results := make(chan sessiontools.Reply, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.call(t, "wio_create_session", sessiontools.Params{RequestID: "same", Engine: "claude", Prompt: "test"})
		}()
	}
	wg.Wait()
	close(results)
	id := ""
	for result := range results {
		if result.Error != nil || result.Session == nil {
			t.Fatalf("concurrent create: %+v", result)
		}
		if id != "" && id != result.Session.ID {
			t.Fatal("created duplicate")
		}
		id = result.Session.ID
	}
	var count int
	if err := f.db.DB.GetContext(context.Background(), &count, "SELECT COUNT(*) FROM claude_threads"); err != nil || count != 1 {
		t.Fatalf("duplicate rows: %d %v", count, err)
	}
}

func TestCollaborationWorktreeWorkflowSurvivesReconciliation(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	p := sessiontools.Params{RequestID: "worktree", Engine: "claude", DirectoryMode: "worktree", Prompt: "fix test"}
	created := f.call(t, "wio_create_session", p)
	if created.Error != nil || created.Status != "preparing" {
		t.Fatalf("prepare: %+v", created)
	}
	op, err := f.db.Operation(ctx, created.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.GitWorktreeCreateCommand
	if err = json.Unmarshal([]byte(op.Payload), &command); err != nil {
		t.Fatal(err)
	}
	if err = f.db.CommitGitWorktree(ctx, command, protocol.GitWorktreeCreateResult{Path: command.TargetPath, Branch: command.Branch, CommitSHA: "abc"}); err != nil {
		t.Fatal(err)
	}
	if err = f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = f.db.ReconcileCollaboration(ctx, f.server); err != nil {
			t.Fatal(err)
		}
	}
	result := f.call(t, "wio_get_request", sessiontools.Params{RequestID: p.RequestID})
	if result.Error != nil || result.Status != "queued" || result.Session == nil {
		t.Fatalf("prepared: %+v", result)
	}
	child, err := f.db.ClaudeThread(ctx, result.Session.ID)
	if err != nil || child.WorkspaceID != command.TargetWorkspaceID {
		t.Fatalf("target workspace: %+v %v", child, err)
	}
	var count int
	if err = f.db.DB.GetContext(ctx, &count, f.db.Q("SELECT COUNT(*) FROM agent_operations WHERE kind='claude.turn.start' AND server_id=?"), f.server); err != nil || count != 1 {
		t.Fatalf("duplicate initial prompt: %d %v", count, err)
	}
}

func TestCollaborationDeferredSelfForkAndWaitCycles(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	reply := f.call(t, "wio_fork_session", sessiontools.Params{RequestID: "fork", Session: f.source, ForkMode: "native"})
	if reply.Error == nil || reply.Error.Code != "SOURCE_BUSY" {
		t.Fatalf("busy fork: %+v", reply)
	}
	reply = f.call(t, "wio_fork_session", sessiontools.Params{RequestID: "deferred", Session: f.source, ForkMode: "after_turn"})
	if reply.Error != nil || reply.Status != "accepted" {
		t.Fatalf("defer: %+v", reply)
	}
	if err := f.db.ReconcileCollaboration(ctx, f.server); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.DB.ExecContext(ctx, f.db.Q("UPDATE codex_threads SET status='idle',codex_thread_id='native-source' WHERE id=?"), f.source.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReconcileCollaboration(ctx, f.server); err != nil {
		t.Fatal(err)
	}
	var r CollaborationRequest
	if err := f.db.DB.GetContext(ctx, &r, "SELECT * FROM session_tool_requests WHERE request_key='deferred'"); err != nil || r.Phase != "fork" || r.OperationID == "" {
		t.Fatalf("deferred advancement: %+v %v", r, err)
	}
	a := sessiontools.Ref{Engine: "codex", ID: "a"}
	b := sessiontools.Ref{Engine: "claude", ID: "b"}
	if err := f.db.RegisterCollaborationWait(ctx, a, []sessiontools.Ref{b}, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RegisterCollaborationWait(ctx, b, []sessiontools.Ref{a}, time.Second); err == nil {
		t.Fatal("wait cycle accepted")
	}
}

func TestCollaborationHandoffReplayAndGrantInheritance(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	grant, _ := f.db.CollaborationGrant(ctx, f.source)
	grant.Delegate = true
	if err := f.db.SetCollaborationGrant(ctx, f.source, grant); err != nil {
		t.Fatal(err)
	}
	p := sessiontools.Params{RequestID: "handoff", Session: f.source, Engine: "claude", ForkMode: "handoff", Handoff: "Goal: check tests. Constraint: do not commit.", Prompt: "Continue review"}
	reply := f.call(t, "wio_fork_session", p)
	if reply.Error != nil {
		t.Fatalf("handoff: %+v", reply)
	}
	replay := f.call(t, "wio_fork_session", p)
	if replay.Error != nil || replay.Session == nil || *reply.Session != *replay.Session {
		t.Fatalf("handoff replay: %+v", replay)
	}
	childGrant, err := f.db.CollaborationGrant(ctx, *reply.Session)
	if err != nil || !childGrant.Enabled {
		t.Fatalf("delegation: %+v %v", childGrant, err)
	}
	grant.Enabled = false
	if err = f.db.SetCollaborationGrant(ctx, f.source, grant); err != nil {
		t.Fatal(err)
	}
	childGrant, err = f.db.CollaborationGrant(ctx, *reply.Session)
	if err != nil || childGrant.Enabled {
		t.Fatalf("inherited grant survived revocation: %+v %v", childGrant, err)
	}
}

func TestCollaborationWorktreeEngineMatrix(t *testing.T) {
	assertCollaborationWorktreeEngineMatrix(t, testStore(t))
}

func assertCollaborationWorktreeEngineMatrix(t *testing.T, db *Store) {
	ctx := context.Background()
	for _, source := range []string{"codex", "claude", "gemini"} {
		for _, target := range []string{"codex", "claude", "gemini"} {
			t.Run(source+"-worktree-"+target, func(t *testing.T) {
				f := newCollaborationFixture(t, db, source)
				if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET workspace_write=0 WHERE id=?"), f.operation); err != nil {
					t.Fatal(err)
				}
				reply := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "isolated", Engine: target, DirectoryMode: "worktree", Prompt: "verify once"})
				if reply.Error != nil || reply.Status != "preparing" {
					t.Fatalf("prepare: %+v", reply)
				}
				op, err := db.Operation(ctx, reply.OperationID)
				if err != nil {
					t.Fatal(err)
				}
				var command protocol.GitWorktreeCreateCommand
				if err = json.Unmarshal([]byte(op.Payload), &command); err != nil {
					t.Fatal(err)
				}
				if err = db.CommitGitWorktree(ctx, command, protocol.GitWorktreeCreateResult{Path: command.TargetPath, Branch: command.Branch, CommitSHA: "abc"}); err != nil {
					t.Fatal(err)
				}
				if err = db.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"}); err != nil {
					t.Fatal(err)
				}
				// Recovery must not depend on a connected source Agent.
				if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE servers SET status='offline' WHERE id=?"), f.server); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if err = db.ReconcileAllCollaboration(ctx); err != nil {
						t.Fatal(err)
					}
				}
				child, err := db.CollaborationSession(ctx, *reply.Session)
				if err != nil || child.WorkspaceID != command.TargetWorkspaceID || child.OperationID == "" {
					t.Fatalf("child: %+v %v", child, err)
				}
				var count int
				if err = db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND kind=?"), command.TargetWorkspaceID, target+".turn.start"); err != nil || count != 1 {
					t.Fatalf("initial turn count=%d: %v", count, err)
				}
			})
		}
	}
}

func TestCollaborationHistoricalResultAndQueuedCancellation(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	first := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "first", Prompt: "FIRST_TURN"})
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	if err := f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: first.OperationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetThreadStatus(ctx, first.Session.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.AddEvent(ctx, protocol.StreamEvent{EventID: NewID(), StreamID: first.Session.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"id":"turn-first","status":"completed"}}`)}); err != nil {
		t.Fatal(err)
	}
	second := f.call(t, "wio_send_message", sessiontools.Params{RequestID: "second", Session: *first.Session, Prompt: "SECOND_TURN"})
	if second.Error != nil {
		t.Fatal(second.Error)
	}
	result := f.call(t, "wio_get_result", sessiontools.Params{Session: *first.Session, OperationID: first.OperationID})
	data, ok := result.Data.(map[string]any)
	if result.Error != nil || !ok || data["turn_ended"] != true {
		t.Fatalf("historical result: %+v", result)
	}
	raw, _ := json.Marshal(data["messages"])
	if !strings.Contains(string(raw), "FIRST_TURN") || strings.Contains(string(raw), "SECOND_TURN") {
		t.Fatalf("wrong turn messages: %s", raw)
	}
	stop := f.call(t, "wio_interrupt_session", sessiontools.Params{RequestID: "stop", Session: *first.Session})
	if stop.Error != nil {
		t.Fatal(stop.Error)
	}
	op, err := f.db.Operation(ctx, second.OperationID)
	if err != nil || op.Status != "cancelled" {
		t.Fatalf("queued cancellation: %+v %v", op, err)
	}
}

func TestCollaborationUnreadAndRootContinuationLimit(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	grant, _ := f.db.CollaborationGrant(ctx, f.source)
	grant.AutoContinue = true
	if err := f.db.SetCollaborationGrant(ctx, f.source, grant); err != nil {
		t.Fatal(err)
	}
	children := []sessiontools.Reply{}
	for _, request := range []string{"one", "two"} {
		reply := f.call(t, "wio_create_session", sessiontools.Params{RequestID: request, Prompt: "work", AutoContinue: true})
		if reply.Error != nil {
			t.Fatal(reply.Error)
		}
		children = append(children, reply)
		if err := f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: reply.OperationID, Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		if err := f.db.SetThreadStatus(ctx, reply.Session.ID, "idle"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.DB.ExecContext(ctx, f.db.Q("UPDATE session_subscriptions SET continuation_count=9 WHERE target_key=?"), children[0].Session.Key()); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetThreadStatus(ctx, f.source.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.notifyCollaborationPair(ctx, f.source.Key(), children[0].Session.Key()); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetThreadStatus(ctx, f.source.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.notifyCollaborationPair(ctx, f.source.Key(), children[1].Session.Key()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.db.DB.GetContext(ctx, &count, f.db.Q("SELECT SUM(continuation_count) FROM session_subscriptions WHERE source_key=?"), f.source.Key()); err != nil || count != 10 {
		t.Fatalf("root continuation count=%d: %v", count, err)
	}
	state, err := f.db.ReadCollaborationState(ctx, f.source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(state["notifications"])
	if !strings.Contains(string(raw), children[0].OperationID) || !strings.Contains(string(raw), children[1].OperationID) {
		t.Fatalf("unread notifications: %s", raw)
	}
	// An old browser acknowledgment must not consume a newer notification.
	if err = f.db.MarkCollaborationRead(ctx, f.source, children[0].Session.Key(), "stale-event"); err != nil {
		t.Fatal(err)
	}
	var unread string
	if err = f.db.DB.GetContext(ctx, &unread, f.db.Q("SELECT unread_event_id FROM session_subscriptions WHERE target_key=?"), children[0].Session.Key()); err != nil || unread == "" {
		t.Fatalf("stale ack erased unread: %v", err)
	}
	if err = f.db.MarkCollaborationRead(ctx, f.source, children[0].Session.Key(), unread); err != nil {
		t.Fatal(err)
	}
	if err = f.db.DB.GetContext(ctx, &unread, f.db.Q("SELECT unread_event_id FROM session_subscriptions WHERE target_key=?"), children[0].Session.Key()); err != nil || unread != "" {
		t.Fatalf("ack failed: %v", err)
	}
}

func TestCollaborationCodexAcceptanceIsNotTurnCompletion(t *testing.T) {
	assertCollaborationCodexTurnCompletion(t, testStore(t))
}

func assertCollaborationCodexTurnCompletion(t *testing.T, db *Store) {
	for _, outcome := range []struct{ native, request, session string }{
		{"completed", "succeeded", "idle"},
		{"failed", "failed", "failed"},
		{"interrupted", "cancelled", "interrupted"},
	} {
		t.Run(outcome.native, func(t *testing.T) {
			f := newCollaborationFixture(t, db, "codex")
			ctx := context.Background()
			grant, err := f.db.CollaborationGrant(ctx, f.source)
			if err != nil {
				t.Fatal(err)
			}
			grant.AutoContinue = true
			if err = f.db.SetCollaborationGrant(ctx, f.source, grant); err != nil {
				t.Fatal(err)
			}
			child := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "child", Prompt: "work", AutoContinue: true})
			if child.Error != nil {
				t.Fatal(child.Error)
			}
			if err = f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: child.OperationID, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			if err = f.db.SetThreadStatus(ctx, child.Session.ID, "running"); err != nil {
				t.Fatal(err)
			}
			assertState := func(status string, ended bool) {
				t.Helper()
				if err := f.db.ReconcileAllCollaboration(ctx); err != nil {
					t.Fatal(err)
				}
				request := f.call(t, "wio_get_request", sessiontools.Params{RequestID: "child"})
				if request.Error != nil || request.Status != status {
					t.Fatalf("request: %+v, want %s", request, status)
				}
				result := f.call(t, "wio_get_result", sessiontools.Params{Session: *child.Session})
				data, ok := result.Data.(map[string]any)
				if result.Error != nil || !ok || data["turn_ended"] != ended || data["turn_status"] != status {
					t.Fatalf("turn: %+v, want %s ended=%v", result, status, ended)
				}
			}
			assertState("running", false)
			// Losing run confirmation must not produce a completion notification.
			if _, err = f.db.DB.ExecContext(ctx, f.db.Q("UPDATE agent_operations SET status='delivered' WHERE id=?"), child.OperationID); err != nil {
				t.Fatal(err)
			}
			if err = f.db.SetThreadStatus(ctx, child.Session.ID, "interrupted"); err != nil {
				t.Fatal(err)
			}
			assertState("unknown", false)
			var unread string
			if err = f.db.DB.GetContext(ctx, &unread, f.db.Q("SELECT unread_event_id FROM session_subscriptions WHERE target_key=?"), child.Session.Key()); err != nil || unread != "" {
				t.Fatalf("premature unread result: %q %v", unread, err)
			}
			if err = f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: child.OperationID, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"turn": map[string]string{"id": "native-turn", "status": outcome.native}})
			if _, err = f.db.AddEvent(ctx, protocol.StreamEvent{EventID: NewID(), StreamID: child.Session.ID, Kind: "codex.turn.completed", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if err = f.db.SetThreadStatus(ctx, child.Session.ID, outcome.session); err != nil {
				t.Fatal(err)
			}
			assertState(outcome.request, true)
			if err = f.db.SetThreadStatus(ctx, f.source.ID, "idle"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err = f.db.notifyCollaborationPair(ctx, f.source.Key(), child.Session.Key()); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err = f.db.DB.GetContext(ctx, &count, f.db.Q("SELECT continuation_count FROM session_subscriptions WHERE target_key=?"), child.Session.Key()); err != nil || count != 1 {
				t.Fatalf("continuation count=%d: %v", count, err)
			}
		})
	}
}

func TestCollaborationInterruptRequestsTrackExecution(t *testing.T) {
	for _, engine := range []string{"claude", "gemini"} {
		for _, delivered := range []bool{false, true} {
			name := engine + "-queued"
			if delivered {
				name = engine + "-delivered"
			}
			t.Run(name, func(t *testing.T) {
				f := newCollaborationFixture(t, testStore(t), "codex")
				ctx := context.Background()
				child := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "child", Engine: engine, Prompt: "work"})
				if child.Error != nil {
					t.Fatal(child.Error)
				}
				if delivered {
					if _, err := f.db.DB.ExecContext(ctx, f.db.Q("UPDATE agent_operations SET status='delivered' WHERE id=?"), child.OperationID); err != nil {
						t.Fatal(err)
					}
				}
				stop := f.call(t, "wio_interrupt_session", sessiontools.Params{RequestID: "stop", Session: *child.Session})
				if stop.Error != nil || stop.OperationID == "" {
					t.Fatalf("untrackable interrupt: %+v", stop)
				}
				if delivered {
					op, err := f.db.Operation(ctx, stop.OperationID)
					if err != nil || op.Kind != engine+".turn.interrupt" || op.Status != "queued" {
						t.Fatalf("interrupt operation: %+v %v", op, err)
					}
					invalid := f.call(t, "wio_get_result", sessiontools.Params{Session: *child.Session, OperationID: stop.OperationID})
					if invalid.Error == nil || invalid.Error.Code != "INVALID_ARGUMENT" {
						t.Fatalf("control operation accepted as a turn: %+v", invalid)
					}
					if err = f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: stop.OperationID, Status: "succeeded"}); err != nil {
						t.Fatal(err)
					}
				} else if stop.OperationID != child.OperationID {
					t.Fatalf("queued cancellation did not identify original execution: %+v", stop)
				}
				if err := f.db.ReconcileAllCollaboration(ctx); err != nil {
					t.Fatal(err)
				}
				request := f.call(t, "wio_get_request", sessiontools.Params{RequestID: "stop"})
				want := "cancelled"
				if delivered {
					want = "succeeded"
				}
				if request.Error != nil || request.Status != want {
					t.Fatalf("interrupt never resolved: %+v, want %s", request, want)
				}
			})
		}
	}
}

func TestCollaborationCodexWorktreeForkHonorsMessageBoundary(t *testing.T) {
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	origin := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "origin"})
	if origin.Error != nil {
		t.Fatal(origin.Error)
	}
	if _, err := f.db.DB.ExecContext(ctx, f.db.Q("UPDATE codex_threads SET codex_thread_id='native-origin' WHERE id=?"), origin.Session.ID); err != nil {
		t.Fatal(err)
	}
	var selected protocol.StreamEvent
	for _, event := range []protocol.StreamEvent{
		{Kind: "user.message", Payload: json.RawMessage(`{"text":"keep question"}`)},
		{Kind: "codex.item.completed", Payload: json.RawMessage(`{"turnId":"keep","item":{"id":"answer","type":"agentMessage","text":"keep answer"}}`)},
		{Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"id":"keep","status":"completed"}}`)},
		{Kind: "user.message", Payload: json.RawMessage(`{"text":"do not copy"}`)},
	} {
		event.StreamID = origin.Session.ID
		saved, err := f.db.AddEvent(ctx, event)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == "codex.item.completed" {
			selected = saved
		}
	}
	fork := f.call(t, "wio_fork_session", sessiontools.Params{RequestID: "fork", Session: *origin.Session, ForkMode: "native", DirectoryMode: "worktree", EventID: selected.EventID})
	if fork.Error != nil {
		t.Fatal(fork.Error)
	}
	op, err := f.db.Operation(ctx, fork.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.GitWorktreeCreateCommand
	if err = json.Unmarshal([]byte(op.Payload), &command); err != nil || command.LastTurnID != "keep" || command.CutoffSequence != selected.Sequence+1 {
		t.Fatalf("lost fork boundary: %+v %v", command, err)
	}
	result := protocol.GitWorktreeCreateResult{Path: command.TargetPath, Branch: command.Branch, CommitSHA: "abc", CodexThread: "native-fork"}
	if err = f.db.CommitGitWorktree(ctx, command, result); err == nil {
		t.Fatal("unconfirmed native boundary accepted")
	}
	result.LastTurnID = "keep"
	if err = f.db.CommitGitWorktree(ctx, command, result); err != nil {
		t.Fatal(err)
	}
	events, err := f.db.Events(ctx, fork.Session.ID, 0, 100)
	if err != nil || len(events) != 3 {
		t.Fatalf("copied wrong history: %+v %v", events, err)
	}
}
