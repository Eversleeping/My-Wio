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

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiYoloPersistsAndDispatchesWithoutApprovals(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "yolo-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.71"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/yolo", Name: "yolo"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %+v %v", workspaces, err)
	}
	api := resourceTestAPI(db)
	input := map[string]string{"workspace_id": workspaces[0].ID, "model": "flash", "approval_mode": "yolo"}
	create := func() *httptest.ResponseRecorder {
		return directJSONRequest(t, "POST", "/api/gemini/threads", input, &store.Session{UserID: "test-user"}, api.createGeminiThread)
	}
	if w := create(); w.Code != 409 || !strings.Contains(w.Body.String(), buildinfo.MinimumGeminiYoloVersion) {
		t.Fatalf("old Agent accepted yolo: %d %s", w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: buildinfo.MinimumGeminiYoloVersion}); err != nil {
		t.Fatal(err)
	}
	w := create()
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var thread store.GeminiThread
	if err := json.Unmarshal(w.Body.Bytes(), &thread); err != nil || thread.ApprovalMode != "yolo" {
		t.Fatalf("yolo not saved: %+v %v", thread, err)
	}
	path := "/api/gemini/threads/" + thread.ID + "/turns"
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.71"}); err != nil {
		t.Fatal(err)
	}
	if w := threadResourceRequest(t, "POST", path, thread.ID, map[string]string{"prompt": "old agent"}, api.startGeminiTurn); w.Code != 409 {
		t.Fatalf("old Agent accepted inherited yolo: %d %s", w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: buildinfo.MinimumGeminiYoloVersion}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "default", "yolo"} {
		w := threadResourceRequest(t, "POST", path, thread.ID, map[string]string{"prompt": "run commands", "approval_mode": mode}, api.startGeminiTurn)
		if w.Code != 202 {
			t.Fatalf("turn: %d %s", w.Code, w.Body.String())
		}
		current, err := db.GeminiThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		op, err := db.Operation(ctx, current.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		var command protocol.GeminiTurnCommand
		want := mode
		if want == "" {
			want = "yolo"
		}
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil || command.ApprovalMode != want || current.ApprovalMode != want {
			t.Fatalf("approval mode lost: %+v %+v %v", current, command, err)
		}
		if err := db.InterruptGeminiTurn(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGeminiSessionLifecycleThroughAuthenticatedRouter(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "gemini-server-token")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.60"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/gemini", Name: "gemini"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	u := store.User{ID: "gemini-user", Username: "gemini-user", AuthMode: store.AuthModePassword}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, u.ID, store.HashToken("gemini-cookie"), "gemini-csrf", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	handler := New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "gemini-cookie"})
			r.Header.Set("X-CSRF-Token", "gemini-csrf")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/workspaces/"+workspaces[0].ID+"/gemini/models", "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/gemini/threads/missing/events/missing/rewrite", `{"prompt":"edit"}`, false); w.Code != 401 {
		t.Fatalf("rewrite route bypassed authentication: %d", w.Code)
	}
	if w := request("GET", "/api/gemini/threads", "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/gemini/threads", `{"workspace_id":"missing"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, options := range []string{`"model":"--help"`, `"approval_mode":"invalid"`} {
		if w := request("POST", "/api/gemini/threads", `{"workspace_id":"`+workspaces[0].ID+`",`+options+`}`, true); w.Code != 400 {
			t.Fatalf("options accepted: %s", w.Body.String())
		}
	}
	w := request("POST", "/api/gemini/threads", `{"workspace_id":"`+workspaces[0].ID+`","model":"flash"}`, true)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var thread store.GeminiThread
	if err := json.Unmarshal(w.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	path := "/api/gemini/threads/" + thread.ID
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE servers SET last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-time.Hour), server.ID); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", path+"/turns", `{"prompt":"offline"}`, true); w.Code != 409 {
		t.Fatalf("offline server accepted turn: %d", w.Code)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.59"}); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", path+"/turns", `{"prompt":"old agent"}`, true); w.Code != 409 {
		t.Fatalf("old Agent accepted Gemini turn: %d", w.Code)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.60"}); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/api/threads/"+thread.ID, "", true); w.Code != 404 {
		t.Fatalf("Gemini leaked into Codex: %d", w.Code)
	}
	if w := request("POST", path+"/turns", `{"prompt":"检查项目","model":"pro","approval_mode":"auto_edit"}`, true); w.Code != 202 {
		t.Fatalf("turn: %d %s", w.Code, w.Body.String())
	}
	claimed, _ := db.GeminiThread(ctx, thread.ID)
	op, err := db.Operation(ctx, claimed.OperationID)
	if err != nil || op.Kind != "gemini.turn.start" || op.WorkspaceWrite != 1 {
		t.Fatalf("operation: %+v %v", op, err)
	}
	var command protocol.GeminiTurnCommand
	_ = json.Unmarshal([]byte(op.Payload), &command)
	if command.Model != "pro" || command.ApprovalMode != "auto_edit" || command.Workspace != thread.Path {
		t.Fatalf("command: %+v", command)
	}
	for method, suffix := range map[string]string{"POST": "/turns", "PATCH": "", "DELETE": ""} {
		body := `{}`
		if method == "POST" {
			body = `{"prompt":"duplicate"}`
		}
		if method == "PATCH" {
			body = `{"title":"busy"}`
		}
		if w := request(method, path+suffix, body, true); w.Code != 409 {
			t.Fatalf("active %s: %d", method, w.Code)
		}
	}
	events, _ := db.Events(ctx, thread.ID, 0, 500)
	users := 0
	for _, e := range events {
		if e.Kind == "gemini.user.message" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("duplicate messages: %d", users)
	}
	if w := request("POST", path+"/interrupt", `{}`, true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	current, _ := db.GeminiThread(ctx, thread.ID)
	if current.Status != "interrupted" {
		t.Fatalf("queued cancellation: %+v", current)
	}
	if w := request("PATCH", path, `{"archived":true,"title":"历史"}`, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request("POST", path+"/turns", `{"prompt":"archived"}`, true); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/gemini/threads?archived=true", "", true); !strings.Contains(w.Body.String(), "历史") {
		t.Fatal(w.Body.String())
	}
	if w := request("GET", path+"/events?after=bad", "", true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := request("DELETE", path, `{}`, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	events, _ = db.Events(ctx, thread.ID, 0, 500)
	// The deletion notification is persisted after the history is removed.
	if len(events) != 1 || events[0].Kind != "gemini.thread.deleted" {
		t.Fatalf("history remains: %+v", events)
	}
}

func TestGeminiManualStopConfirmationIsBoundToTheLostTurn(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "gemini-confirm-agent")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/confirm", Name: "confirm"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, _ := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if _, err := db.QueueGeminiTurn(ctx, thread, "work", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	if err := db.MarkRunning(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET status='interrupted' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	path := "/api/gemini/threads/" + thread.ID + "/confirm-stopped"
	request := func(body map[string]any) *httptest.ResponseRecorder {
		return threadResourceRequest(t, "POST", path, thread.ID, body, api.confirmGeminiStopped)
	}
	if w := request(map[string]any{"operation_id": thread.OperationID}); w.Code != 400 {
		t.Fatalf("missing confirmation accepted: %d", w.Code)
	}
	if w := request(map[string]any{"operation_id": "stale", "confirmed": true}); w.Code != 409 {
		t.Fatalf("stale operation accepted: %d", w.Code)
	}
	before, _ := db.GeminiThread(ctx, thread.ID)
	if !before.RecoveryPending {
		t.Fatal("invalid confirmation released workspace")
	}
	if w := request(map[string]any{"operation_id": thread.OperationID, "confirmed": true}); w.Code != 200 {
		t.Fatalf("confirmed stop rejected: %d %s", w.Code, w.Body.String())
	}
	after, _ := db.GeminiThread(ctx, thread.ID)
	if after.Status != "interrupted" || after.RecoveryPending {
		t.Fatalf("manual confirmation did not release workspace: %+v", after)
	}
	events, _ := db.Events(ctx, thread.ID, 0, 500)
	if events[len(events)-1].Kind != "gemini.turn.completed" {
		t.Fatalf("missing completion event: %+v", events)
	}
	if _, err := db.QueueGeminiTurn(ctx, after, "next", "auto", "default"); err != nil {
		t.Fatalf("workspace was not released: %v", err)
	}
	if w := request(map[string]any{"operation_id": thread.OperationID, "confirmed": true}); w.Code != 409 {
		t.Fatalf("stale confirmation changed a new turn: %d", w.Code)
	}
}
