package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func newSetupTestHandler(t *testing.T, log *slog.Logger, options ...Option) (http.Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return setupTestHandlerFor(database, log, options...), database
}

func setupTestHandlerFor(database *store.Store, log *slog.Logger, options ...Option) http.Handler {
	vault := security.DevVault()
	hub := realtime.New()
	return New(database, hub, agentgateway.New(database, hub, vault, log), vault, log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true, options...)
}

func discardTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// loggedSetupToken returns the token from the first-run warning, or "".
func loggedSetupToken(t *testing.T, logs []byte) string {
	t.Helper()
	for _, line := range bytes.Split(logs, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Level      string `json:"level"`
			SetupToken string `json:"setup_token"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("invalid log line %q: %v", line, err)
		}
		if entry.SetupToken != "" {
			if entry.Level != "WARN" {
				t.Fatalf("setup token logged at level %q", entry.Level)
			}
			return entry.SetupToken
		}
	}
	return ""
}

func setupBody(token string) map[string]string {
	body := map[string]string{"username": "admin", "auth_mode": store.AuthModePassword, "password": "correct-horse-battery"}
	if token != "" {
		body["setup_token"] = token
	}
	return body
}

func TestSetupRequiresValidToken(t *testing.T) {
	handler, _ := newSetupTestHandler(t, discardTestLog(), WithSetupToken("expected-token"))
	for name, token := range map[string]string{"missing": "", "wrong": "wrong-token", "prefix": "expected"} {
		t.Run(name, func(t *testing.T) {
			response := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody(token), nil)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "setup token") {
				t.Fatalf("setup returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	status := requestJSON(t, handler, http.MethodGet, "/api/setup/status", nil, nil)
	if !strings.Contains(status.Body.String(), `"configured":false`) {
		t.Fatalf("rejected setup created an administrator: %s", status.Body.String())
	}
	if created := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody("expected-token"), nil); created.Code != http.StatusCreated {
		t.Fatalf("setup with valid token returned %d: %s", created.Code, created.Body.String())
	}
	// Once an administrator exists the token grants nothing, whichever is sent.
	for _, token := range []string{"expected-token", "wrong-token", ""} {
		if again := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody(token), nil); again.Code != http.StatusConflict {
			t.Fatalf("setup after configuration with %q returned %d: %s", token, again.Code, again.Body.String())
		}
	}
}

func TestSetupTokenFromEnvironmentIsRequired(t *testing.T) {
	t.Setenv("WIO_SETUP_TOKEN", "  operator-chosen-token\n")
	logs := &lockedBuffer{}
	handler, _ := newSetupTestHandler(t, slog.New(slog.NewJSONHandler(logs, nil)))
	if token := loggedSetupToken(t, logs.Bytes()); token != "" {
		t.Fatalf("configured token should not be replaced by a generated one: %q", token)
	}
	if wrong := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody("generated"), nil); wrong.Code != http.StatusForbidden {
		t.Fatalf("wrong token returned %d: %s", wrong.Code, wrong.Body.String())
	}
	if created := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody("operator-chosen-token"), nil); created.Code != http.StatusCreated {
		t.Fatalf("environment token returned %d: %s", created.Code, created.Body.String())
	}
}

func TestSetupTokenIsGeneratedAndLoggedOnlyWhileUnconfigured(t *testing.T) {
	t.Setenv("WIO_SETUP_TOKEN", "")
	logs := &lockedBuffer{}
	handler, database := newSetupTestHandler(t, slog.New(slog.NewJSONHandler(logs, nil)))
	token := loggedSetupToken(t, logs.Bytes())
	if len(token) < 32 {
		t.Fatalf("generated setup token %q was not logged", token)
	}
	if missing := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody(""), nil); missing.Code != http.StatusForbidden {
		t.Fatalf("missing token returned %d: %s", missing.Code, missing.Body.String())
	}
	if created := requestJSON(t, handler, http.MethodPost, "/api/setup", setupBody(token), nil); created.Code != http.StatusCreated {
		t.Fatalf("generated token returned %d: %s", created.Code, created.Body.String())
	}
	restartLogs := &lockedBuffer{}
	setupTestHandlerFor(database, slog.New(slog.NewJSONHandler(restartLogs, nil)))
	if generated := loggedSetupToken(t, restartLogs.Bytes()); generated != "" {
		t.Fatalf("configured deployment still generated token %q", generated)
	}
}

func setupTOTPAdmin(t *testing.T, handler http.Handler, mode, password string) (string, []string) {
	t.Helper()
	setup := requestJSON(t, handler, http.MethodPost, "/api/setup", map[string]string{"setup_token": testSetupToken, "username": "admin", "auth_mode": mode, "password": password}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", setup.Code, setup.Body.String())
	}
	var body struct {
		Secret        string   `json:"totp_secret"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &body); err != nil || body.Secret == "" {
		t.Fatalf("unexpected setup response: %v %s", err, setup.Body.String())
	}
	return body.Secret, body.RecoveryCodes
}

func totpCodeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	handler, _ := newSetupTestHandler(t, discardTestLog(), WithSetupToken(testSetupToken))
	secret, _ := setupTOTPAdmin(t, handler, store.AuthModeTOTP, "")
	login := func(code string) int {
		return requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "code": code}, nil).Code
	}
	now := time.Now()
	code := totpCodeAt(t, secret, now)
	if status := login(code); status != http.StatusOK {
		t.Fatalf("first use returned %d", status)
	}
	if status := login(code); status != http.StatusUnauthorized {
		t.Fatalf("replayed code returned %d", status)
	}
	if status := login(totpCodeAt(t, secret, now.Add(30*time.Second))); status != http.StatusOK {
		t.Fatalf("next time step within clock skew returned %d", status)
	}
	// An older step stays refused even though it was never used itself.
	if status := login(totpCodeAt(t, secret, now.Add(-30*time.Second))); status != http.StatusUnauthorized {
		t.Fatalf("older time step returned %d", status)
	}
}

func TestWrongPasswordDoesNotSpendTOTPCode(t *testing.T) {
	handler, _ := newSetupTestHandler(t, discardTestLog(), WithSetupToken(testSetupToken))
	const password = "correct-horse-battery-staple"
	secret, _ := setupTOTPAdmin(t, handler, store.AuthModePasswordTOTP, password)
	code := totpCodeAt(t, secret, time.Now())
	wrong := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": "incorrect-password", "code": code}, nil)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password returned %d", wrong.Code)
	}
	login := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": password, "code": code}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("code was spent by a failed attempt: %d %s", login.Code, login.Body.String())
	}
}

func TestResponsesCarryContentSecurityPolicy(t *testing.T) {
	handler, _ := newSetupTestHandler(t, discardTestLog(), WithSetupToken(testSetupToken))
	for _, target := range []string{"/", "/api/health", "/assets/app.js"} {
		response := requestJSON(t, handler, http.MethodGet, target, nil, nil)
		policy := response.Header().Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self' 'unsafe-inline'", "img-src 'self' data: blob:", "connect-src 'self'", "object-src 'none'", "base-uri 'self'", "form-action 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(policy, directive) {
				t.Fatalf("%s policy %q is missing %q", target, policy, directive)
			}
		}
		if strings.Contains(policy, "unsafe-eval") || strings.Contains(policy, "*") {
			t.Fatalf("%s policy is too permissive: %q", target, policy)
		}
	}
}
