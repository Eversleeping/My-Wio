package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestClaudeAuthenticatedSessionAndAgentVersion(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "claude-api")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.89"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude", Name: "claude"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	user := store.User{ID: "claude-user", Username: "claude-user", AuthMode: store.AuthModePassword}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user.ID, store.HashToken("claude-cookie"), "claude-csrf", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	handler := New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "claude-cookie"})
			r.Header.Set("X-CSRF-Token", "claude-csrf")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/claude/threads", "", false); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	createBody, _ := json.Marshal(map[string]string{"workspace_id": workspaces[0].ID, "model": "sonnet", "permission_mode": "default", "effort": "low"})
	w := request("POST", "/api/claude/threads", string(createBody), true)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var thread store.ClaudeThread
	if err := json.Unmarshal(w.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Effort != "low" {
		t.Fatalf("created effort: %+v", thread)
	}
	path := "/api/claude/threads/" + thread.ID + "/turns"
	if w := request("POST", path, `{"prompt":"test"}`, true); w.Code != 409 || !strings.Contains(w.Body.String(), "0.2.90") {
		t.Fatalf("old Agent: %d %s", w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.90"}); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", path, `{"prompt":"test","permission_mode":"invented"}`, true); w.Code != 400 {
		t.Fatalf("invalid mode: %d %s", w.Code, w.Body.String())
	}
	for _, value := range []string{"ultra", "--help", "HIGH"} {
		body, _ := json.Marshal(map[string]string{"prompt": "test", "effort": value})
		if w := request("POST", path, string(body), true); w.Code != 400 {
			t.Fatalf("invalid effort: %d %s", w.Code, w.Body.String())
		}
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.91"}); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", path, `{"prompt":"test","effort":"max"}`, true); w.Code != 409 || !strings.Contains(w.Body.String(), "0.2.92") {
		t.Fatalf("old effort Agent: %d %s", w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.92"}); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", path, `{"prompt":"test","model":"opus[1m]","permission_mode":"acceptEdits","effort":"max"}`, true); w.Code != 202 {
		t.Fatalf("turn: %d %s", w.Code, w.Body.String())
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	op, _ := db.Operation(ctx, thread.OperationID)
	var command protocol.ClaudeTurnCommand
	if json.Unmarshal([]byte(op.Payload), &command) != nil || command.Model != "opus[1m]" || command.PermissionMode != "acceptEdits" || command.Effort != "max" || thread.Effort != "max" || !thread.EffortSupported {
		t.Fatalf("command: %+v", command)
	}
	if w := request("POST", path, `{"prompt":"duplicate"}`, true); w.Code != 409 {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/interrupt", `{}`, true); w.Code != 202 {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", path, `{"prompt":"continue"}`, true); w.Code != 202 {
		t.Fatalf("inherited effort turn: %d %s", w.Code, w.Body.String())
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	op, _ = db.Operation(ctx, thread.OperationID)
	if json.Unmarshal([]byte(op.Payload), &command) != nil || command.Effort != "max" {
		t.Fatalf("effort not inherited: %+v", command)
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/interrupt", `{}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if w := request("POST", path, `{"prompt":"reset","effort":""}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	op, _ = db.Operation(ctx, thread.OperationID)
	command = protocol.ClaudeTurnCommand{}
	if json.Unmarshal([]byte(op.Payload), &command) != nil || command.Effort != "" || thread.Effort != "" {
		t.Fatalf("effort not cleared: %+v", command)
	}

	if w := request("POST", "/api/claude/threads/"+thread.ID+"/interrupt", `{}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	imageBody := `{"images":[{"data_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII="}]}`
	if w := request("POST", path, imageBody, true); w.Code != 409 || !strings.Contains(w.Body.String(), "0.2.93") {
		t.Fatalf("old Agent accepted images: %d %s", w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.93"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{"images":[{"data_url":"data:image/png;base64,aGVsbG8="}]}`, `{"images":[{"data_url":"https://example.com/test.png"}]}`, `{"images":[{"data_url":"data:image/svg+xml;base64,PHN2Zz4="}]}`} {
		if w := request("POST", path, invalid, true); w.Code != 400 {
			t.Fatalf("invalid image accepted: %d", w.Code)
		}
	}
	if w := request("POST", path, imageBody, true); w.Code != 202 {
		t.Fatalf("image-only turn: %d %s", w.Code, w.Body.String())
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	op, _ = db.Operation(ctx, thread.OperationID)
	command = protocol.ClaudeTurnCommand{}
	if json.Unmarshal([]byte(op.Payload), &command) != nil || len(command.Images) != 1 || command.Prompt != "" {
		t.Fatalf("images lost: %+v", command)
	}
	if w := request("PATCH", "/api/claude/threads/"+thread.ID, `{"pinned":true}`, true); w.Code != 200 {
		t.Fatalf("active pin: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/fork", `{}`, true); w.Code != 409 {
		t.Fatal("forked active thread")
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/interrupt", `{}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET session_id=? WHERE id=?"), "550e8400-e29b-41d4-a716-446655440000", thread.ID); err != nil {
		t.Fatal(err)
	}
	history, err := db.Events(ctx, thread.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var target protocol.StreamEvent
	for _, event := range history {
		if event.Kind == "claude.user.message" {
			target = event
		}
	}
	rewritePath := "/api/claude/threads/" + thread.ID + "/events/" + target.EventID + "/rewrite"
	if w := request("POST", rewritePath, imageBody, false); w.Code != 401 {
		t.Fatalf("unauthenticated rewrite: %d", w.Code)
	}
	if w := request("POST", rewritePath, imageBody, true); w.Code != 409 || !strings.Contains(w.Body.String(), "0.2.97") {
		t.Fatalf("old rewrite Agent: %d %s", w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.97"}); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", rewritePath, `{"prompt":""}`, true); w.Code != 400 {
		t.Fatalf("empty edit: %d", w.Code)
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/events/foreign/rewrite", `{"prompt":"edit"}`, true); w.Code != 409 {
		t.Fatalf("foreign edit: %d", w.Code)
	}
	if w := request("POST", rewritePath, imageBody, true); w.Code != 202 {
		t.Fatalf("image-only rewrite: %d %s", w.Code, w.Body.String())
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	op, _ = db.Operation(ctx, thread.OperationID)
	command = protocol.ClaudeTurnCommand{}
	if json.Unmarshal([]byte(op.Payload), &command) != nil || command.Rewrite == nil || command.Rewrite.EventID != target.EventID || len(command.Images) != 1 {
		t.Fatalf("rewrite command: %+v", command)
	}
	if w := request("POST", rewritePath, imageBody, true); w.Code != 409 {
		t.Fatal("rewrote an active thread")
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/interrupt", `{}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	after, _ := db.Events(ctx, thread.ID, 0, 500)
	retained := map[string]bool{}
	for _, event := range after {
		retained[event.EventID] = true
	}
	for _, event := range history {
		if !retained[event.EventID] {
			t.Fatal("cancelled edit removed history", event.EventID)
		}
	}
	if w := request("POST", "/api/claude/threads/"+thread.ID+"/fork", `{}`, true); w.Code != 202 {
		t.Fatalf("fork: %d %s", w.Code, w.Body.String())
	}
	scheduleBody := `{"agent_kind":"claude","thread_id":"` + thread.ID + `","name":"review","prompt":"review","schedule":"@hourly","timezone":"Asia/Shanghai","reasoning_effort":"max","approval_mode":"plan","model":"custom-model"}`
	if w := request("POST", "/api/scheduled-tasks", scheduleBody, true); w.Code != 201 {
		t.Fatalf("Claude schedule: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/scheduled-tasks", strings.ReplaceAll(scheduleBody, `"plan"`, `"never"`), true); w.Code != 400 {
		t.Fatal("accepted invalid Claude permission")
	}
	if w := request("POST", "/api/scheduled-tasks", strings.ReplaceAll(scheduleBody, `"max"`, `"invalid"`), true); w.Code != 400 {
		t.Fatal("accepted invalid Claude effort")
	}

}
