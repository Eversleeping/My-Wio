package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/codexcli"
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

func TestCheckCodexCLIUpdatesSavesLatestStableRelease(t *testing.T) {
	database := openBootstrapTestStore(t)
	if err := database.SetSetting(context.Background(), codexCLITargetSetting, "0.144.4"); err != nil {
		t.Fatal(err)
	}
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"rust-v0.145.0-alpha.1","prerelease":false},
			{"tag_name":"rust-v0.144.5","prerelease":false}
		]`))
	}))
	defer releases.Close()
	api := resourceTestAPI(database)
	api.codexReleases = codexcli.NewReleaseChecker(releases.Client(), releases.URL)
	request := httptest.NewRequest(http.MethodPost, "/api/settings/codex-cli/check-updates", nil)
	request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, store.Session{UserID: "test-user"}))
	response := httptest.NewRecorder()
	api.checkCodexCLIUpdates(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("release check returned %d: %s", response.Code, response.Body.String())
	}
	var result codexCLISettingsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.TargetVersion != "0.144.5" || result.LatestVersion != "0.144.5" || !result.Updated {
		t.Fatalf("unexpected release check result: %#v %v", result, err)
	}
	stored, err := database.Setting(context.Background(), codexCLITargetSetting, "")
	if err != nil || stored != "0.144.5" {
		t.Fatalf("latest release was not saved: %q %v", stored, err)
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
