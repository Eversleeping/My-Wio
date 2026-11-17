package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
)

type fakeAccessManager struct {
	fakeServerBootstrapper
	called  bool
	request sshbootstrap.AccessRequest
}

func (f *fakeAccessManager) Access(_ context.Context, request sshbootstrap.AccessRequest) (sshbootstrap.AccessSettings, error) {
	f.called, f.request = true, request
	if request.Settings != nil {
		return *request.Settings, nil
	}
	return sshbootstrap.AccessSettings{AllowSudo: true, Revision: strings.Repeat("a", 64)}, nil
}

func TestServerAccessReadApplyAndGuards(t *testing.T) {
	db := openBootstrapTestStore(t)
	server, _ := enrollBootstrapTestServer(t, db, "access-node")
	if err := db.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{AgentVersion: buildinfo.Version, Capabilities: []string{protocol.CapabilitySessionIsolation}}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAccessManager{}
	api := &API{store: db, bootstrapper: fake}
	input := map[string]any{"host": "node.test", "port": 22, "user": "root", "auth_method": "password", "password": "synthetic-password", "host_key_fingerprint": "SHA256:test"}
	user := &store.Session{UserID: "test-user"}
	call := func(id string) int {
		t.Helper()
		response := directServerJSONRequest(t, http.MethodPost, "/api/servers/"+id+"/ssh/access-settings", id, input, user, api.serverAccess)
		return response.Code
	}
	if status := call(server.ID); status != 200 || !fake.called || fake.request.ExpectedServerID != server.ID || fake.request.Settings != nil {
		t.Fatalf("read: %d %+v", status, fake.request)
	}
	input["settings"] = sshbootstrap.AccessSettings{SessionIsolation: true, Revision: strings.Repeat("a", 64)}
	if status := call(server.ID); status != 200 || fake.request.Settings == nil || !fake.request.Settings.SessionIsolation {
		t.Fatalf("apply: %d", status)
	}
	for _, settings := range []sshbootstrap.AccessSettings{{AllowSudo: true, SessionIsolation: true, Revision: strings.Repeat("a", 64)}, {AllowSudo: true}} {
		fake.called = false
		input["settings"] = settings
		if status := call(server.ID); status != 400 || fake.called {
			t.Fatalf("invalid settings passed: %d", status)
		}
	}
	input["settings"] = sshbootstrap.AccessSettings{AllowSudo: true, Revision: strings.Repeat("a", 64)}
	if err := db.UpsertInventory(context.Background(), server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/access", Name: "access"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(context.Background())
	if err != nil || len(workspaces) == 0 {
		t.Fatalf("workspaces: %v", err)
	}
	thread, err := db.CreateThread(context.Background(), workspaces[0].ID, "busy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(context.Background(), db.Q("UPDATE codex_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	fake.called = false
	if status := call(server.ID); status != 409 || fake.called {
		t.Fatalf("active session not blocked: %d", status)
	}
	control, err := db.EnsureControlPlaneServer(context.Background(), "control", "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	if status := call(control.ID); status != 400 || fake.called {
		t.Fatalf("built-in Agent not blocked: %d", status)
	}
	if status := call("missing"); status != 404 || fake.called {
		t.Fatalf("missing server: %d", status)
	}
}
