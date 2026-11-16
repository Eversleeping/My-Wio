package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestCollaborationBrowserAuthorization(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "collaboration-agent")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/collaboration", Name: "collaboration"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	thread, err := db.CreateThread(ctx, workspaces[0].ID, "Collaboration")
	if err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "collaboration-user", Username: "collaboration-user", AuthMode: store.AuthModePassword}
	if err = db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSession(ctx, user.ID, store.HashToken("collaboration-cookie"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	handler := New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	path := "/api/sessions/codex/" + thread.ID + "/collaboration"
	request := func(method, path, body string, authenticated, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "collaboration-cookie"})
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", path, "", false, false); w.Code != 401 {
		t.Fatalf("anonymous read: %d", w.Code)
	}
	if w := request("PUT", path, `{"enabled":true}`, true, false); w.Code != 403 {
		t.Fatalf("missing csrf: %d", w.Code)
	}
	if w := request("PUT", path, `{"enabled":true,"worktree":true}`, true, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", path, "", true, false); w.Code != 200 || !strings.Contains(w.Body.String(), `"show_unread_results":false`) || !strings.Contains(w.Body.String(), `"show_creation_results":false`) {
		t.Fatalf("default display preferences: %d %s", w.Code, w.Body.String())
	}
	if w := request("PUT", path, `{"enabled":true,"worktree":true,"show_unread_results":true,"show_creation_results":true}`, true, true); w.Code != 200 {
		t.Fatalf("save display preferences: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", path, "", true, false); w.Code != 200 || !strings.Contains(w.Body.String(), `"show_unread_results":true`) || !strings.Contains(w.Body.String(), `"show_creation_results":true`) {
		t.Fatalf("persisted display preferences: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", path+"/read", `{"target_key":"claude:other","event_id":"old"}`, true, false); w.Code != 403 {
		t.Fatalf("read ack missing csrf: %d", w.Code)
	}
	if w := request("PUT", path, `{"enabled":false}`, true, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
}

// Every route by which the signed-in user sends a session a message gives its
// collaboration group a fresh allowance of automatic continuations. A message
// that is refused leaves the allowance alone.
func TestUserMessageRestoresContinuationAllowance(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "allowance-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: buildinfo.Version, ClaudeVersion: "2.1.318 (Claude Code)", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	// One workspace for each case: a workspace takes one writing turn at a time.
	var repositories []protocol.Repository
	for i := 0; i < 9; i++ {
		repositories = append(repositories, protocol.Repository{Path: fmt.Sprintf("/srv/allowance-%d", i), Name: fmt.Sprintf("allowance-%d", i)})
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: repositories}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != len(repositories) {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	workspace := func() string {
		id := workspaces[0].ID
		workspaces = workspaces[1:]
		return id
	}
	user := store.User{ID: "allowance-user", Username: "allowance-user", AuthMode: store.AuthModePassword}
	if err = db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSession(ctx, user.ID, store.HashToken("allowance-cookie"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	handler := New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	post := func(path string, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "allowance-cookie"})
		r.Header.Set("X-CSRF-Token", "csrf")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	exec := func(t *testing.T, query string, arguments ...any) {
		t.Helper()
		if _, err := db.DB.ExecContext(ctx, db.Q(query), arguments...); err != nil {
			t.Fatal(err)
		}
	}
	event := func(t *testing.T, e protocol.StreamEvent) protocol.StreamEvent {
		t.Helper()
		saved, err := db.AddEvent(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	// spend links child under parent with the group's allowance used up, and
	// returns a function that reports how many continuations are counted now.
	spend := func(t *testing.T, parent, child string) func() int {
		t.Helper()
		request := store.NewID()
		exec(t, "INSERT INTO session_tool_requests(id,source_key,request_key,tool,arguments,grant_version,status) VALUES(?,?,?,'wio_create_session','{}',1,'succeeded')", request, parent, request)
		exec(t, "INSERT INTO session_links(target_key,source_key,root_key,depth,request_id,relation) VALUES(?,?,?,1,?,'created')", child, parent, parent, request)
		exec(t, "INSERT INTO session_subscriptions(target_key,source_key,auto_continue,grant_version,continuation_count) VALUES(?,?,1,1,10)", child, parent)
		return func() int {
			t.Helper()
			var count int
			if err := db.DB.GetContext(ctx, &count, db.Q("SELECT continuation_count FROM session_subscriptions WHERE target_key=? AND source_key=?"), child, parent); err != nil {
				t.Fatal(err)
			}
			return count
		}
	}
	accepted := func(t *testing.T, w *httptest.ResponseRecorder, spent func() int) {
		t.Helper()
		if w.Code != http.StatusAccepted {
			t.Fatalf("message refused: %d %s", w.Code, w.Body.String())
		}
		if count := spent(); count != 0 {
			t.Fatalf("the user's message left %d continuations counted", count)
		}
	}
	codex := func(t *testing.T) store.Thread {
		t.Helper()
		thread, err := db.CreateThread(ctx, workspace(), "Allowance")
		if err != nil {
			t.Fatal(err)
		}
		return thread
	}
	turn := map[string]any{"prompt": "continue", "approval_mode": "on-request"}

	t.Run("codex turn", func(t *testing.T) {
		thread := codex(t)
		spent := spend(t, "codex:"+thread.ID, "codex:"+store.NewID())
		accepted(t, post("/api/sessions/codex/"+thread.ID+"/turns", turn), spent)
	})
	t.Run("codex correction to a running turn", func(t *testing.T) {
		thread := codex(t)
		exec(t, "UPDATE codex_threads SET codex_thread_id=?,status='running' WHERE id=?", "native-"+thread.ID, thread.ID)
		event(t, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.started", Payload: json.RawMessage(`{"turn":{"id":"active-turn"}}`)})
		spent := spend(t, "codex:"+thread.ID, "codex:"+store.NewID())
		accepted(t, post("/api/sessions/codex/"+thread.ID+"/steer", map[string]any{"prompt": "correct", "images": []any{}}), spent)
	})
	t.Run("codex edited message", func(t *testing.T) {
		thread := codex(t)
		target := event(t, protocol.StreamEvent{StreamID: thread.ID, Kind: "user.message", Payload: json.RawMessage(`{"text":"first"}`)})
		event(t, protocol.StreamEvent{StreamID: thread.ID, Kind: "turn.accepted", Payload: json.RawMessage(`{"turn_id":"turn-1"}`)})
		event(t, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"status":"completed"}}`)})
		spent := spend(t, "codex:"+thread.ID, "codex:"+store.NewID())
		accepted(t, post("/api/sessions/codex/"+thread.ID+"/events/"+target.EventID+"/rewrite", map[string]any{"prompt": "revised", "approval_mode": "on-request"}), spent)
	})
	t.Run("claude turn, sent to the child", func(t *testing.T) {
		thread, err := db.CreateClaudeThread(ctx, workspace(), "sonnet", "default", "")
		if err != nil {
			t.Fatal(err)
		}
		// The user may write to any session of the group, here the child.
		spent := spend(t, "codex:"+store.NewID(), "claude:"+thread.ID)
		accepted(t, post("/api/sessions/claude/"+thread.ID+"/turns", map[string]any{"prompt": "continue"}), spent)
	})
	t.Run("claude correction to a running turn", func(t *testing.T) {
		thread, err := db.CreateClaudeThread(ctx, workspace(), "sonnet", "default", "")
		if err != nil {
			t.Fatal(err)
		}
		if w := post("/api/sessions/claude/"+thread.ID+"/turns", map[string]any{"prompt": "start"}); w.Code != http.StatusAccepted {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		if thread, err = db.ClaudeThread(ctx, thread.ID); err != nil {
			t.Fatal(err)
		}
		exec(t, "UPDATE agent_operations SET status='running' WHERE id=?", thread.OperationID)
		exec(t, "UPDATE claude_threads SET status='running' WHERE id=?", thread.ID)
		ready, _ := json.Marshal(map[string]any{"operation_id": thread.OperationID, "ready": true})
		event(t, protocol.StreamEvent{StreamID: thread.ID, Kind: "claude.steer.ready", Payload: ready})
		spent := spend(t, "claude:"+thread.ID, "codex:"+store.NewID())
		accepted(t, post("/api/sessions/claude/"+thread.ID+"/steer", map[string]any{"operation_id": thread.OperationID, "request_id": uuid.NewString(), "prompt": "correct"}), spent)
	})
	t.Run("gemini turn", func(t *testing.T) {
		thread, err := db.CreateGeminiThread(ctx, workspace(), "flash", "default")
		if err != nil {
			t.Fatal(err)
		}
		spent := spend(t, "gemini:"+thread.ID, "codex:"+store.NewID())
		accepted(t, post("/api/sessions/gemini/"+thread.ID+"/turns", map[string]any{"prompt": "continue"}), spent)
	})
	t.Run("gemini correction to a running turn", func(t *testing.T) {
		thread, err := db.CreateGeminiThread(ctx, workspace(), "flash", "default")
		if err != nil {
			t.Fatal(err)
		}
		if w := post("/api/sessions/gemini/"+thread.ID+"/turns", map[string]any{"prompt": "start"}); w.Code != http.StatusAccepted {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		if thread, err = db.GeminiThread(ctx, thread.ID); err != nil {
			t.Fatal(err)
		}
		exec(t, "UPDATE gemini_threads SET status='running' WHERE id=?", thread.ID)
		ready, _ := json.Marshal(map[string]any{"operation_id": thread.OperationID})
		event(t, protocol.StreamEvent{EventID: thread.OperationID + ":steer-ready", StreamID: thread.ID, Kind: "gemini.steer.ready", Payload: ready})
		spent := spend(t, "gemini:"+thread.ID, "codex:"+store.NewID())
		accepted(t, post("/api/sessions/gemini/"+thread.ID+"/steer", map[string]any{"operation_id": thread.OperationID, "request_id": uuid.NewString(), "prompt": "correct"}), spent)
	})
	t.Run("gemini goal resumed by the user", func(t *testing.T) {
		thread, err := db.CreateGeminiThread(ctx, workspace(), "flash", "default")
		if err != nil {
			t.Fatal(err)
		}
		path := "/api/sessions/gemini/" + thread.ID
		if w := post(path+"/turns", map[string]any{"prompt": "Verify the tests", "goal": map[string]int{"max_continuations": 4, "max_seconds": 300}}); w.Code != http.StatusAccepted {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		goal, err := db.GeminiGoal(ctx, thread.ID)
		if err != nil || goal == nil {
			t.Fatalf("goal: %+v %v", goal, err)
		}
		spent := spend(t, "gemini:"+thread.ID, "codex:"+store.NewID())
		// Pausing is not a message.
		if w := post(path+"/goals/"+goal.ID+"/pause", map[string]any{}); w.Code != http.StatusAccepted {
			t.Fatalf("pause: %d %s", w.Code, w.Body.String())
		}
		if count := spent(); count != 10 {
			t.Fatalf("pausing a goal changed the allowance: %d", count)
		}
		accepted(t, post(path+"/goals/"+goal.ID+"/resume", map[string]any{"max_continuations": 5, "max_seconds": 600}), spent)
	})
	t.Run("a refused message leaves the allowance alone", func(t *testing.T) {
		thread := codex(t)
		if w := post("/api/sessions/codex/"+thread.ID+"/turns", turn); w.Code != http.StatusAccepted {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		spent := spend(t, "codex:"+thread.ID, "codex:"+store.NewID())
		if w := post("/api/sessions/codex/"+thread.ID+"/turns", turn); w.Code != http.StatusConflict {
			t.Fatalf("a second turn was accepted while one is active: %d %s", w.Code, w.Body.String())
		}
		if count := spent(); count != 10 {
			t.Fatalf("a refused message changed the allowance: %d", count)
		}
	})
}
