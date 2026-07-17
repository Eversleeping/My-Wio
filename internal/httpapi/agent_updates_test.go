package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/agentupdate"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestUpdateAgentQueuesVerifiedPackageCommand(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "0.3.0"
	t.Cleanup(func() { buildinfo.Version = originalVersion })

	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "update-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	assetDir := t.TempDir()
	packageBody := []byte("signed-agent-package")
	if err := os.WriteFile(filepath.Join(assetDir, "wio-agent-linux-amd64"), packageBody, 0o600); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	api.agentUpdates = agentupdate.New(assetDir)

	response := agentUpdateRequest(t, api, server.ID, &store.Session{UserID: "test-user"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("Agent update returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(context.Background(), server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "agent.update" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.AgentUpdateCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	pkg, ok := command.Packages["amd64"]
	if command.Version != "0.3.0" || !ok || pkg.Size != int64(len(packageBody)) || pkg.SHA256 == "" {
		t.Fatalf("unexpected update command: %#v", command)
	}
	duplicate := agentUpdateRequest(t, api, server.ID, &store.Session{UserID: "test-user"})
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate Agent update returned %d: %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestDownloadAgentUpdateRequiresAgentToken(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "0.3.0"
	t.Cleanup(func() { buildinfo.Version = originalVersion })

	database := openBootstrapTestStore(t)
	_ = enrollResourceTestServer(t, database, "download-token")
	assetDir := t.TempDir()
	packageBody := []byte("agent-binary")
	if err := os.WriteFile(filepath.Join(assetDir, "wio-agent-linux-amd64"), packageBody, 0o600); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	api.agentUpdates = agentupdate.New(assetDir)

	unauthorized := agentPackageRequest(t, api, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized download returned %d", unauthorized.Code)
	}
	authorized := agentPackageRequest(t, api, "download-token-agent")
	if authorized.Code != http.StatusOK || authorized.Body.String() != string(packageBody) {
		t.Fatalf("authorized download returned %d: %q", authorized.Code, authorized.Body.String())
	}
	if authorized.Header().Get("X-Checksum-SHA256") == "" || authorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing package integrity headers: %#v", authorized.Header())
	}
}

func agentUpdateRequest(t *testing.T, api *API, serverID string, session *store.Session) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", serverID)
	request := httptest.NewRequest(http.MethodPost, "/api/servers/"+serverID+"/agent-update", nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	if session != nil {
		request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, *session))
	}
	response := httptest.NewRecorder()
	api.updateAgent(response, request)
	return response
}

func agentPackageRequest(t *testing.T, api *API, token string) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("architecture", "amd64")
	request := httptest.NewRequest(http.MethodGet, "/api/agent/update-package/amd64?version="+buildinfo.Version, nil)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	api.downloadAgentUpdate(response, request)
	return response
}
