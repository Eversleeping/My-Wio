package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
)

type fakeServerBootstrapper struct {
	probeTarget    sshbootstrap.HostTarget
	installRequest sshbootstrap.InstallRequest
	probeResult    sshbootstrap.HostKeyResult
	installResult  sshbootstrap.InstallResult
	installError   error
}

func (fake *fakeServerBootstrapper) Probe(_ context.Context, target sshbootstrap.HostTarget) (sshbootstrap.HostKeyResult, error) {
	fake.probeTarget = target
	return fake.probeResult, nil
}

func (fake *fakeServerBootstrapper) Install(_ context.Context, request sshbootstrap.InstallRequest) (sshbootstrap.InstallResult, error) {
	fake.installRequest = request
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
	for _, secret := range []string{"ssh-secret", "api-key-secret", "private-key-secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response exposed %q", secret)
		}
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

func bootstrapInput() map[string]any {
	return map[string]any{
		"name": "node-1", "scan_roots": []string{"/srv"}, "host": "192.0.2.10", "port": 22, "user": "ubuntu",
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
