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

func TestUsageRoutesRequireAuthenticationAndRespectSessionType(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "usage-test-agent")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/usage", Name: "usage"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	codex, err := db.CreateThread(ctx, workspaces[0].ID, "Codex")
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "usage-user", Username: "usage-user", AuthMode: store.AuthModePassword}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user.ID, store.HashToken("usage-cookie"), "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	handler := New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	request := func(path string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "usage-cookie"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/threads/" + codex.ID + "/usage", "/api/gemini/threads/" + gemini.ID + "/usage"} {
		if w := request(path, false); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
		if w := request(path, true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"input_tokens":null`) {
			t.Fatalf("empty usage %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/threads/" + gemini.ID + "/usage", "/api/gemini/threads/" + codex.ID + "/usage"} {
		if w := request(path, true); w.Code != http.StatusNotFound {
			t.Fatalf("cross-provider %s: %d", path, w.Code)
		}
	}
}
