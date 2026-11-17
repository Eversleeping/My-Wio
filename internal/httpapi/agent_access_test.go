package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestAgentAccessQueuesWithoutSSHAndRejectsUnsafeChanges(t *testing.T) {
	db := openBootstrapTestStore(t)
	server, _ := enrollBootstrapTestServer(t, db, "agent-access-node")
	if err := db.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{AgentVersion: buildinfo.Version, Capabilities: []string{protocol.CapabilityAgentAccess}}); err != nil {
		t.Fatal(err)
	}
	// Use the real gateway from a test handler; no SSH bootstrapper is configured.
	api := resourceTestAPI(db)
	user := &store.Session{UserID: "test-user"}
	request := func(method string, input any) int {
		return directServerJSONRequest(t, method, "/api/servers/"+server.ID+"/access-settings", server.ID, input, user, api.agentAccess).Code
	}
	if status := request(http.MethodPost, map[string]any{}); status != 202 {
		t.Fatalf("read: %d", status)
	}
	settings := protocol.AgentAccessSettings{AllowSudo: true, Revision: strings.Repeat("a", 64)}
	if status := request(http.MethodPut, settings); status != 202 {
		t.Fatalf("apply: %d", status)
	}
	ops, err := db.PendingOperations(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.AgentAccessCommand
	found := false
	for _, op := range ops {
		if op.Kind == protocol.OpAgentAccessUpdate {
			if json.Unmarshal([]byte(op.Payload), &got) != nil {
				t.Fatal("invalid payload")
			}
			found = true
		}
	}
	if !found || got.ServerID != server.ID || got.Settings == nil || !got.Settings.AllowSudo {
		t.Fatalf("missing authenticated Agent command: %+v", got)
	}
	if status := request(http.MethodPut, settings); status != 409 {
		t.Fatalf("duplicate pending write: %d", status)
	}
	settings.SessionIsolation = true
	if status := request(http.MethodPut, settings); status != 400 {
		t.Fatalf("conflict: %d", status)
	}
	if err := db.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{AgentVersion: buildinfo.Version, Capabilities: []string{}}); err != nil {
		t.Fatal(err)
	}
	if status := request(http.MethodPost, map[string]any{}); status != 409 {
		t.Fatalf("old Agent: %d", status)
	}
}

func TestAgentAccessResultScopesAndFiltersAgentData(t *testing.T) {
	db := openBootstrapTestStore(t)
	server, _ := enrollBootstrapTestServer(t, db, "agent-access-result")
	api := resourceTestAPI(db)
	ctx := context.Background()
	for _, tc := range []struct {
		name, kind, serverID, data string
		status                     int
	}{
		{"safe settings only", protocol.OpAgentAccessRead, server.ID, `{"allow_sudo":true,"revision":"` + strings.Repeat("a", 64) + `","agent_token":"must-not-leak"}`, 200},
		{"wrong server", protocol.OpAgentAccessRead, "another-server", `{}`, 404},
		{"wrong operation", protocol.OpAgentUpdate, server.ID, `{}`, 404},
		{"missing revision", protocol.OpAgentAccessRead, server.ID, `{}`, 500},
		{"conflicting settings", protocol.OpAgentAccessUpdate, server.ID, `{"allow_sudo":true,"session_isolation":true,"revision":"` + strings.Repeat("a", 64) + `"}`, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := db.QueueResourceOperation(ctx, server.ID, tc.kind, map[string]any{}, store.NewID(), store.OperationResource{}, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: id, Status: "succeeded", Data: json.RawMessage(tc.data)}); err != nil {
				t.Fatal(err)
			}
			route := chi.NewRouteContext()
			route.URLParams.Add("serverID", tc.serverID)
			route.URLParams.Add("operationID", id)
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
			w := httptest.NewRecorder()
			api.agentAccessResult(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "must-not-leak") || strings.Contains(w.Body.String(), "agent_token") {
				t.Fatalf("unsafe or unexpected result: %d %s", w.Code, w.Body.String())
			}
			if tc.status == 200 && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("runtime settings result must not be cached")
			}
		})
	}
}
