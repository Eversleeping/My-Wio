package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
)

type fakeServerBootstrapper struct {
	probeTarget     sshbootstrap.HostTarget
	installRequest  sshbootstrap.InstallRequest
	probeResult     sshbootstrap.HostKeyResult
	installResult   sshbootstrap.InstallResult
	installError    error
	installProgress []sshbootstrap.InstallProgress
	installDeadline bool
}

func (fake *fakeServerBootstrapper) Probe(_ context.Context, target sshbootstrap.HostTarget) (sshbootstrap.HostKeyResult, error) {
	fake.probeTarget = target
	return fake.probeResult, nil
}

func (fake *fakeServerBootstrapper) Install(ctx context.Context, request sshbootstrap.InstallRequest) (sshbootstrap.InstallResult, error) {
	fake.installRequest = request
	_, fake.installDeadline = ctx.Deadline()
	for _, progress := range fake.installProgress {
		if request.Progress != nil {
			request.Progress(progress)
		}
	}
	return fake.installResult, fake.installError
}

func TestProbeServerSSHOnlyRequiresHost(t *testing.T) {
	fake := &fakeServerBootstrapper{probeResult: sshbootstrap.HostKeyResult{Fingerprint: "SHA256:test", KeyType: "ssh-ed25519"}}
	api := &API{bootstrapper: fake}
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/probe", map[string]any{"host": "192.0.2.10", "port": 2222}, nil, api.probeServerSSH)
	if response.Code != http.StatusOK {
		t.Fatalf("probe returned %d: %s", response.Code, response.Body.String())
	}
	if fake.probeTarget.Host != "192.0.2.10" || fake.probeTarget.Port != 2222 {
		t.Fatalf("unexpected probe target: %#v", fake.probeTarget)
	}
}

func TestBootstrapServerSSHDoesNotEchoSecrets(t *testing.T) {
	database := openBootstrapTestStore(t)
	fake := &fakeServerBootstrapper{installResult: sshbootstrap.InstallResult{ServerID: "server-id", Hostname: "node-1", Architecture: "arm64"}}
	api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	input := bootstrapInput()
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/bootstrap", input, &store.Session{UserID: "test-user"}, api.bootstrapServerSSH)
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap returned %d: %s", response.Code, response.Body.String())
	}
	if fake.installRequest.ControlURL != "https://wio.example.com" || fake.installRequest.CodexAPIURL != "https://api.example.com/v1" {
		t.Fatalf("unexpected install request: %#v", fake.installRequest)
	}
	if fake.installRequest.EnrollmentToken == "" {
		t.Fatal("missing enrollment token")
	}
	if fake.installDeadline {
		t.Fatal("server installation must not inherit a fixed request deadline")
	}
	for _, secret := range []string{"ssh-secret", "api-key-secret", "private-key-secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response exposed %q", secret)
		}
	}
	enrollment, err := database.ConsumeEnrollment(context.Background(), fake.installRequest.EnrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Address != "192.0.2.10" || enrollment.Configuration != "4 vCPU / 8 GB RAM" || enrollment.Notes != "Production API" {
		t.Fatalf("server metadata was not attached to enrollment: %#v", enrollment)
	}
}

func TestBootstrapFailureDeletesUnusedEnrollment(t *testing.T) {
	database := openBootstrapTestStore(t)
	fake := &fakeServerBootstrapper{installError: errors.New("installation failed")}
	api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/bootstrap", bootstrapInput(), nil, api.bootstrapServerSSH)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("bootstrap returned %d: %s", response.Code, response.Body.String())
	}
	var count int
	if err := database.DB.Get(&count, "SELECT COUNT(*) FROM enrollment_tokens"); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected enrollment cleanup, found %d", count)
	}
}

func TestRepairBootstrapCreatesEnrollmentForExistingServer(t *testing.T) {
	database := openBootstrapTestStore(t)
	if _, err := database.CreateEnrollment(context.Background(), "repair-node", []string{"/srv"}, "initial-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	initial, err := database.ConsumeEnrollment(context.Background(), "initial-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(context.Background(), initial, "repair-node.local", "initial-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeServerBootstrapper{installResult: sshbootstrap.InstallResult{ServerID: server.ID, Hostname: "repair-node.local", Architecture: "amd64"}}
	api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	input := bootstrapInput()
	input["allow_sudo"] = true
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/servers/"+server.ID+"/ssh/repair", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("serverID", server.ID)
	requestContext := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	request = request.WithContext(requestContext)
	response := httptest.NewRecorder()
	api.repairServerSSH(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("repair bootstrap returned %d: %s", response.Code, response.Body.String())
	}
	repair, err := database.ConsumeEnrollment(context.Background(), fake.installRequest.EnrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	if repair.ServerID != server.ID {
		t.Fatalf("repair enrollment targets %q, want %q", repair.ServerID, server.ID)
	}
	if !fake.installRequest.AllowSudo {
		t.Fatal("repair did not preserve the optional sudo setting")
	}
}

func TestBootstrapAuthenticationFailureHasSpecificCode(t *testing.T) {
	database := openBootstrapTestStore(t)
	fake := &fakeServerBootstrapper{installError: fmt.Errorf("%w: password rejected", sshbootstrap.ErrAuthentication)}
	api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/bootstrap", bootstrapInput(), nil, api.bootstrapServerSSH)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bootstrap returned %d: %s", response.Code, response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "ssh_auth_failed" {
		t.Fatalf("unexpected error code: %q", body["code"])
	}
}

func TestBootstrapStreamEmitsProgressAndResult(t *testing.T) {
	database := openBootstrapTestStore(t)
	fake := &fakeServerBootstrapper{
		installResult: sshbootstrap.InstallResult{ServerID: "server-id", Hostname: "node-1", Architecture: "amd64"},
		installProgress: []sshbootstrap.InstallProgress{
			{Step: "connecting"},
			{Step: "uploading_agent", Current: 512, Total: 1024},
		},
	}
	api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/bootstrap-stream", bootstrapInput(), &store.Session{UserID: "test-user"}, api.streamBootstrapServerSSH)
	if response.Code != http.StatusOK || !response.Flushed {
		t.Fatalf("stream returned %d, flushed=%v: %s", response.Code, response.Flushed, response.Body.String())
	}
	var events []bootstrapStreamEvent
	decoder := json.NewDecoder(response.Body)
	for {
		var event bootstrapStreamEvent
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 4 || events[2].Step != "uploading_agent" || events[2].Current != 512 {
		t.Fatalf("unexpected progress events: %#v", events)
	}
	if events[3].Type != "complete" || events[3].Result == nil || events[3].Result.ServerID != "server-id" {
		t.Fatalf("missing completion event: %#v", events)
	}
}

func TestBootstrapStreamEmitsSafeInstallationError(t *testing.T) {
	database := openBootstrapTestStore(t)
	fake := &fakeServerBootstrapper{installError: fmt.Errorf("%w: upload closed by remote host", sshbootstrap.ErrInstallation)}
	api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/bootstrap-stream", bootstrapInput(), nil, api.streamBootstrapServerSSH)
	var last bootstrapStreamEvent
	decoder := json.NewDecoder(response.Body)
	for decoder.Decode(&last) == nil {
	}
	if last.Type != "error" || last.Code != "installation_failed" || !strings.Contains(last.Detail, "remote host") {
		t.Fatalf("unexpected error event: %#v", last)
	}
	for _, secret := range []string{"ssh-secret", "api-key-secret", "private-key-secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("stream exposed %q", secret)
		}
	}
}

func bootstrapInput() map[string]any {
	return map[string]any{
		"name": "node-1", "scan_roots": []string{"/srv"}, "host": "192.0.2.10", "port": 22, "user": "ubuntu",
		"configuration": "4 vCPU / 8 GB RAM", "notes": "Production API",
		"auth_method": "password", "password": "ssh-secret", "private_key": "private-key-secret", "private_key_passphrase": "",
		"host_key_fingerprint": "SHA256:test", "codex_api_url": "https://api.example.com/v1", "codex_api_key": "api-key-secret", "codex_model": "gpt-5.4",
	}
}

func openBootstrapTestStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func directJSONRequest(t *testing.T, method, target string, body any, session *store.Session, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if session != nil {
		request = request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, *session))
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}
