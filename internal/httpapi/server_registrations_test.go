package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/agentupdate"
	"github.com/wio-platform/wio/internal/installer"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func registrationTestAPI(t *testing.T) (*API, store.CredentialProfile) {
	t.Helper()
	db := openBootstrapTestStore(t)
	vault := security.DevVault()
	secret, err := vault.Encrypt("api-key-registration-secret")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "codex", Name: "registration-preset", Endpoint: "https://api.example.com/v1", Model: "gpt-6.1-sol"}, secret)
	if err != nil {
		t.Fatal(err)
	}
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "wio-agent-linux-amd64"), []byte("installer-package"), 0600); err != nil {
		t.Fatal(err)
	}
	return &API{store: db, vault: vault, agentUpdates: agentupdate.New(assets), publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}, profile
}
func registrationRequest(t *testing.T, handler http.HandlerFunc, id, key, method string, input any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "https://wio.example.com/api/test", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("registrationID", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, sessionContextKey{}, store.Session{UserID: "test-user"})
	response := httptest.NewRecorder()
	handler(response, req.WithContext(ctx))
	return response
}
func TestCommandRegistrationEquivalentConfigurationAndHeartbeat(t *testing.T) {
	api, profile := registrationTestAPI(t)
	ctx := context.Background()
	created := registrationRequest(t, api.createServerRegistration, "", "", "POST", registrationConfiguration{Name: "command-node", ScanRoots: []string{"/srv"}, CodexProfileID: profile.ID, Notes: "server-note", OS: "linux"})
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var command struct {
		Registration store.ServerRegistration `json:"registration"`
		Command      string                   `json:"command"`
		Code         string                   `json:"code"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &command); err != nil {
		t.Fatal(err)
	}
	if command.Code == "" || strings.Contains(command.Command, command.Code) || strings.Contains(created.Body.String(), "api-key-registration-secret") {
		t.Fatal("command exposed a secret or omitted one-time code")
	}
	assertGeneratedRegistrationCommand(t, command.Command)
	id := command.Registration.ID
	key := strings.Repeat("a", 64)
	persisted, err := api.store.ServerRegistration(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted.ConfigJSON, "api-key-registration-secret") {
		t.Fatal("registration persisted plaintext preset")
	}
	// Preset edits after issuance must not silently change the installation plan.
	replacement, _ := api.vault.Encrypt("replacement-key")
	if _, err := api.store.SaveCredentialProfile(ctx, profile, replacement); err != nil {
		t.Fatal(err)
	}
	claim := registrationRequest(t, api.claimServerRegistration, id, "", "POST", map[string]string{"code": command.Code, "device_key": key, "os": "linux", "architecture": "amd64"})
	if claim.Code != 200 {
		t.Fatalf("claim: %d %s", claim.Code, claim.Body.String())
	}
	var plan struct {
		Request installer.InstallRequest `json:"request"`
	}
	if err := json.Unmarshal(claim.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Request.CodexAPIKey != "api-key-registration-secret" || plan.Request.CodexAPIURL != profile.Endpoint || plan.Request.CodexModel != profile.Model {
		t.Fatal("bound preset was not applied")
	}
	other := registrationRequest(t, api.claimServerRegistration, id, "", "POST", map[string]string{"code": command.Code, "device_key": strings.Repeat("b", 64), "os": "linux", "architecture": "amd64"})
	if other.Code != 401 {
		t.Fatal("another device replayed claimed code")
	}
	session := registrationRequest(t, api.installationSession, id, key, "GET", nil)
	if session.Code != 200 {
		t.Fatal(session.Body.String())
	}
	var enrolled map[string]any
	for attempt := 0; attempt < 2; attempt++ {
		response := registrationRequest(t, api.enrollRegisteredAgent, id, key, "POST", map[string]string{"hostname": "node.local"})
		if response.Code != 201 {
			t.Fatalf("enroll: %d %s", response.Code, response.Body.String())
		}
		var next map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &next); err != nil {
			t.Fatal(err)
		}
		if attempt > 0 && (next["server_id"] != enrolled["server_id"] || next["agent_token"] != enrolled["agent_token"]) {
			t.Fatal("lost-response retry duplicated identity")
		}
		enrolled = next
	}
	serverID := enrolled["server_id"].(string)
	server, err := api.store.Server(ctx, serverID)
	if err != nil || server.CodexProfileID != profile.ID || server.Notes != "server-note" || server.ScanRoots != `["/srv"]` {
		t.Fatalf("registered metadata/profiles differ: %+v %v", server, err)
	}
	invalid := registrationRequest(t, api.installationProgress, id, key, "POST", installer.Event{Type: "progress", Step: "api-key-registration-secret"})
	if invalid.Code != 400 {
		t.Fatal("arbitrary secret-bearing progress was accepted")
	}
	progress := registrationRequest(t, api.installationProgress, id, key, "POST", installer.Event{Type: "progress", Step: "starting_service"})
	if progress.Code != 200 {
		t.Fatal(progress.Body.String())
	}
	complete := registrationRequest(t, api.installationProgress, id, key, "POST", installer.Event{Type: "complete", Result: &installer.InstallResult{ServerID: serverID, Architecture: "amd64", Warnings: []string{}}})
	if complete.Code != 200 {
		t.Fatal(complete.Body.String())
	}
	before := registrationRequest(t, api.serverRegistration, id, "", "GET", nil)
	var state store.ServerRegistration
	if err := json.Unmarshal(before.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "waiting_online" {
		t.Fatalf("completion was accepted without heartbeat: %s", state.State)
	}
	if err := api.store.Heartbeat(ctx, serverID, protocol.Heartbeat{Hostname: "node.local", AgentVersion: "0.3.46", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	after := registrationRequest(t, api.serverRegistration, id, "", "GET", nil)
	if err := json.Unmarshal(after.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State != "complete" {
		t.Fatalf("heartbeat did not complete registration: %s", state.State)
	}
	if strings.Contains(after.Body.String(), key) || strings.Contains(after.Body.String(), "api-key-registration-secret") || strings.Contains(after.Body.String(), command.Code) {
		t.Fatal("progress view exposed credentials")
	}
}
func TestInstallScriptPinsPackageAndUsesNoCodeInArguments(t *testing.T) {
	api, _ := registrationTestAPI(t)
	response := httptest.NewRecorder()
	api.agentInstallScript(response, httptest.NewRequest("GET", "/api/agent/install.sh", nil))
	script := response.Body.String()
	if response.Code != 200 || !strings.Contains(script, "sha256sum -c -") || !strings.Contains(script, "--registration") || strings.Contains(script, "--token") {
		t.Fatalf("unsafe installer script: %s", script)
	}
	if !strings.Contains(script, "/run/systemd/system") || !strings.Contains(script, "mktemp -d") {
		t.Fatal("installer skipped preflight or private staging")
	}
}
func TestRegistrationRejectsOtherPlatformsAndRevokedSessions(t *testing.T) {
	api, profile := registrationTestAPI(t)
	bad := registrationRequest(t, api.createServerRegistration, "", "", "POST", registrationConfiguration{Name: "windows", OS: "windows", CodexProfileID: profile.ID})
	if bad.Code != 400 {
		t.Fatal("unsupported platform accepted")
	}
	row, err := api.store.CreateServerRegistration(context.Background(), "revoked", "code", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("x", 64)
	if _, err := api.store.ClaimServerRegistration(context.Background(), row.ID, "code", key); err != nil {
		t.Fatal(err)
	}
	if err := api.store.RevokeServerRegistration(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.HandlerFunc{api.installationSession, api.enrollRegisteredAgent, api.installationProgress} {
		response := registrationRequest(t, handler, row.ID, key, "POST", map[string]string{"hostname": "node"})
		if response.Code != 401 {
			t.Fatalf("revoked session accepted: %d", response.Code)
		}
	}
}

func TestRegisteredAgentEnrollmentWire(t *testing.T) {
	api, profile := registrationTestAPI(t)
	ctx := context.Background()
	key := strings.Repeat("f", 64)
	protected, err := api.vault.Encrypt(registrationSnapshot{AgentToken: "wire-agent-token"})
	if err != nil {
		t.Fatal(err)
	}
	configuration := registrationConfiguration{Name: "wire-node", ScanRoots: []string{"/srv"}, CodexProfileID: profile.ID, CredentialsCiphertext: protected}
	raw, _ := json.Marshal(configuration)
	row, err := api.store.CreateServerRegistration(ctx, "wire-node", "code", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.store.ClaimServerRegistration(ctx, row.ID, "code", key); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Post("/api/agent/registrations/{registrationID}/enroll", api.enrollRegisteredAgent)
	server := httptest.NewServer(router)
	defer server.Close()
	state := t.TempDir()
	options := agent.EnrollmentOptions{ControlURL: server.URL, RegistrationID: row.ID, EnrollmentToken: key, ConfigPath: filepath.Join(state, "config.json"), StateDir: state, CloneRoot: filepath.Join(state, "projects")}
	first, err := agent.Enroll(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := agent.Enroll(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.ServerID == "" || first.ServerID != again.ServerID || first.AgentToken != again.AgentToken || len(first.ScanRoots) != 1 {
		t.Fatal("Agent enrollment request lost identity or scan roots")
	}
}

func TestInstallerStatusRequiresAgentCredentialsAndRealHeartbeat(t *testing.T) {
	api, _ := registrationTestAPI(t)
	server, token := enrollBootstrapTestServer(t, api.store, "install-status-node")
	rejected := registrationRequest(t, api.agentInstallStatus, "", "wrong-agent-token", "GET", nil)
	if rejected.Code != 401 {
		t.Fatal("installer status accepted invalid Agent credentials")
	}
	offline := registrationRequest(t, api.agentInstallStatus, "", token, "GET", nil)
	var result struct {
		Online   bool   `json:"online"`
		ServerID string `json:"server_id"`
	}
	if offline.Code != 200 || json.Unmarshal(offline.Body.Bytes(), &result) != nil || result.Online || result.ServerID != server.ID {
		t.Fatalf("offline server falsely verified: %s", offline.Body.String())
	}
	if err := api.store.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node.local", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	online := registrationRequest(t, api.agentInstallStatus, "", token, "GET", nil)
	if online.Code != 200 || json.Unmarshal(online.Body.Bytes(), &result) != nil || !result.Online {
		t.Fatal("real heartbeat did not verify installation")
	}
}

func TestRegistrationStorageFailureIsRetryable(t *testing.T) {
	api, _ := registrationTestAPI(t)
	if err := api.store.Close(); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	session := registrationRequest(t, api.installationSession, "11111111-1111-4111-8111-111111111111", key, "GET", nil)
	if session.Code != 503 {
		t.Fatalf("storage failure looked like an expired device credential: %d", session.Code)
	}
	claim := registrationRequest(t, api.claimServerRegistration, "11111111-1111-4111-8111-111111111111", "", "POST", map[string]string{"code": "code", "device_key": key, "os": "linux", "architecture": "amd64"})
	if claim.Code != 503 {
		t.Fatalf("storage failure looked like an invalid code: %d", claim.Code)
	}
}

// Execute only fake curl/id/sh/sudo programs to verify root and sudo dispatch,
// argument quoting, and piped stdin without downloading or installing anything.
func assertGeneratedRegistrationCommand(t *testing.T, command string) {
	t.Helper()
	for _, uid := range []string{"0", "1000"} {
		directory := t.TempDir()
		trace := filepath.Join(directory, "trace")
		scripts := map[string]string{
			"id":   "#!/bin/sh\nprintf '%s' " + uid + "\n",
			"curl": "#!/bin/sh\nprintf 'installer-payload\\n'\n",
			"sh":   "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' \"$@\" > \"$TRACE\"\n",
			"sudo": "#!/bin/sh\nexec \"$@\"\n",
		}
		for name, body := range scripts {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
		process.Env = []string{"PATH=" + directory + ":/usr/bin:/bin", "TRACE=" + trace}
		output, err := process.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("generated command failed for uid %s: %s %v", uid, output, err)
		}
		raw, err := os.ReadFile(trace)
		if err != nil || !strings.Contains(string(raw), "--registration") || !strings.Contains(string(raw), "https://wio.example.com") {
			t.Fatalf("command lost installation arguments for uid %s: %s %v", uid, raw, err)
		}
	}
}
