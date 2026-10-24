package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func cliRequest(a *API, id, kind, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", id)
	route.URLParams.Add("kind", kind)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, sessionContextKey{}, store.Session{UserID: "test-user"})
	w := httptest.NewRecorder()
	a.installServerCLI(w, r.WithContext(ctx))
	return w
}

func TestCLIInstallAndStatusPreserveExplicitVersion(t *testing.T) {
	db := openBootstrapTestStore(t)
	s := enrollResourceTestServer(t, db, "cli-install-token")
	if err := db.Heartbeat(context.Background(), s.ID, protocol.Heartbeat{AgentVersion: "0.2.61", GeminiReady: true, GeminiVersion: "0.65.0"}); err != nil {
		t.Fatal(err)
	}
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"tag_name":"v0.65.0"},{"tag_name":"v0.64.0"},{"tag_name":"v0.66.0-preview.1"}]`))
	}))
	defer releases.Close()
	a := resourceTestAPI(db)
	a.geminiReleases = codexcli.NewTaggedReleaseChecker(releases.Client(), releases.URL, "v")
	for _, body := range []string{`{"version":"0.66.0-preview.1"}`, `{"version":"0.63.0"}`, `{"version":"../../evil"}`} {
		if w := cliRequest(a, s.ID, "gemini", body); w.Code != 400 {
			t.Fatalf("invalid version: %d %s", w.Code, w.Body)
		}
	}
	if w := cliRequest(a, s.ID, "gemini", `{"version":"0.65.0"}`); w.Code != 409 {
		t.Fatalf("same version: %d %s", w.Code, w.Body)
	}
	if w := cliRequest(a, s.ID, "gemini", `{"version":"0.64.0"}`); w.Code != 202 {
		t.Fatalf("version switch: %d %s", w.Code, w.Body)
	}
	if w := cliRequest(a, s.ID, "gemini", `{"version":"0.64.0"}`); w.Code != 409 {
		t.Fatalf("duplicate queue: %d %s", w.Code, w.Body)
	}
	ops, err := db.PendingOperations(context.Background(), s.ID)
	if err != nil || len(ops) != 1 {
		t.Fatalf("operations: %#v %v", ops, err)
	}
	var command protocol.CLIInstallCommand
	if err := json.Unmarshal([]byte(ops[0].Payload), &command); err != nil || command.Kind != "gemini" || command.Version != "0.64.0" {
		t.Fatalf("command: %#v %v", command, err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", s.ID)
	w := httptest.NewRecorder()
	a.serverCLIs(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route)))
	if w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	var statuses []serverCLI
	if err := json.Unmarshal(w.Body.Bytes(), &statuses); err != nil || len(statuses) != 2 || statuses[1].Operation == nil || statuses[1].Version != "0.65.0" || !statuses[1].Ready {
		t.Fatalf("statuses: %#v %v", statuses, err)
	}
}

func TestCLIInstallRejectsOldOfflineAndBusyAgents(t *testing.T) {
	db := openBootstrapTestStore(t)
	s := enrollResourceTestServer(t, db, "cli-guards-token")
	a := resourceTestAPI(db)
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"tag_name":"v0.65.0"}]`)) }))
	defer releases.Close()
	a.geminiReleases = codexcli.NewTaggedReleaseChecker(releases.Client(), releases.URL, "v")
	if w := cliRequest(a, s.ID, "gemini", `{"version":"0.65.0"}`); w.Code != 409 {
		t.Fatalf("offline: %d", w.Code)
	}
	db.Heartbeat(context.Background(), s.ID, protocol.Heartbeat{AgentVersion: "0.2.60"})
	if w := cliRequest(a, s.ID, "gemini", `{"version":"0.65.0"}`); w.Code != 409 {
		t.Fatalf("old Agent: %d", w.Code)
	}
	db.Heartbeat(context.Background(), s.ID, protocol.Heartbeat{AgentVersion: "0.2.61"})
	if _, err := db.QueueOperation(context.Background(), s.ID, "codex.update", protocol.CodexUpdateCommand{Version: "0.162.1"}, "existing-update"); err != nil {
		t.Fatal(err)
	}
	if w := cliRequest(a, s.ID, "gemini", `{"version":"0.65.0"}`); w.Code != 409 {
		t.Fatalf("busy install: %d %s", w.Code, w.Body)
	}
}

func TestCLIInstallRejectsActiveGeminiTurns(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	s := enrollResourceTestServer(t, db, "cli-active-turn-token")
	if err := db.Heartbeat(ctx, s.ID, protocol.Heartbeat{AgentVersion: "0.2.61"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, s.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/cli-test", Name: "cli-test"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatal(err)
	}
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueGeminiTurn(ctx, thread, "private test prompt", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueCLIInstall(ctx, s.ID, protocol.CLIInstallCommand{Kind: "gemini", Version: "0.65.0"}); err != store.ErrCLIInstallBusy {
		t.Fatalf("active Gemini turn allowed installation: %v", err)
	}
}
