package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
