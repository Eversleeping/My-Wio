package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestCodexUpdateQueuesConfiguredTargetVersion(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "codex-update-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.9", CodexVersion: "codex-cli 0.139.0", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetSetting(context.Background(), codexCLITargetSetting, "0.144.4"); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := codexUpdateRequest(t, api, server.ID)
	if response.Code != http.StatusAccepted {
		t.Fatalf("Codex update returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(context.Background(), server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.update" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.CodexUpdateCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.Version != "0.144.4" {
		t.Fatalf("unexpected command: %#v %v", command, err)
	}
	if duplicate := codexUpdateRequest(t, api, server.ID); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate update returned %d: %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestCodexTargetSettingRejectsNonSemanticVersion(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	request := httptest.NewRequest(http.MethodPost, "/api/settings/codex-cli", strings.NewReader(`{"target_version":"0.144.4; shutdown"}`))
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, store.Session{UserID: "test-user"}))
	response := httptest.NewRecorder()
	api.saveCodexCLISettings(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid target returned %d: %s", response.Code, response.Body.String())
	}
}

func TestCodexUpdateRequiresCapableAgent(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "old-agent-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.8", CodexVersion: "codex-cli 0.139.0", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := codexUpdateRequest(t, api, server.ID)
	if response.Code != http.StatusConflict {
		t.Fatalf("legacy Agent update returned %d: %s", response.Code, response.Body.String())
	}
}

func codexUpdateRequest(t *testing.T, api *API, serverID string) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", serverID)
	request := httptest.NewRequest(http.MethodPost, "/api/servers/"+serverID+"/codex-update", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, store.Session{UserID: "test-user"}))
	response := httptest.NewRecorder()
	api.updateCodexCLI(response, request)
	return response
}
