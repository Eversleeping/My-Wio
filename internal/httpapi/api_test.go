package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestSetupLoginAndAuthenticatedSession(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	vault := security.DevVault()
	hub := realtime.New()
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	gateway := agentgateway.New(database, hub, vault, log)
	frontend, _ := fs.Sub(fstest.MapFS{"index.html": {Data: []byte("ok")}}, ".")
	handler := New(database, hub, gateway, vault, log, frontend, "http://localhost", true)

	setup := requestJSON(t, handler, http.MethodPost, "/api/setup", map[string]string{"username": "admin", "password": "correct horse battery staple"}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", setup.Code, setup.Body.String())
	}
	var setupBody struct {
		Secret string `json:"totp_secret"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &setupBody); err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(setupBody.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	login := requestJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": "correct horse battery staple", "code": code}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", login.Code, login.Body.String())
	}
	var loginBody store.Session
	var sessionBody struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &sessionBody); err != nil || sessionBody.CSRF == "" {
		t.Fatalf("missing CSRF token: %v %s", err, login.Body.String())
	}
	_ = loginBody
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("missing session cookie: %#v", cookies)
	}
	session := requestJSON(t, handler, http.MethodGet, "/api/auth/session", nil, cookies[0])
	if session.Code != http.StatusOK {
		t.Fatalf("session returned %d: %s", session.Code, session.Body.String())
	}
	logoutWithoutCSRF := requestJSON(t, handler, http.MethodPost, "/api/auth/logout", nil, cookies[0])
	if logoutWithoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF returned %d", logoutWithoutCSRF.Code)
	}
}

func TestServiceWorkerIsNotLongTermCached(t *testing.T) {
	api := &API{frontend: fstest.MapFS{
		"index.html": {Data: []byte("index")},
		"sw-v2.js":   {Data: []byte("worker")},
	}}
	for _, requestPath := range []string{"/sw-v2.js", "/sw.js"} {
		t.Run(requestPath, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, requestPath, nil)
			response := httptest.NewRecorder()
			api.serveFrontend(response, request)
			if response.Code != http.StatusOK || response.Body.String() != "worker" {
				t.Fatalf("service worker returned %d: %q", response.Code, response.Body.String())
			}
			if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store, no-cache, must-revalidate" {
				t.Fatalf("service worker cache control = %q", cacheControl)
			}
			if cdnCacheControl := response.Header().Get("CDN-Cache-Control"); cdnCacheControl != "no-store" {
				t.Fatalf("service worker CDN cache control = %q", cdnCacheControl)
			}
			if cloudflareCacheControl := response.Header().Get("Cloudflare-CDN-Cache-Control"); cloudflareCacheControl != "no-store" {
				t.Fatalf("service worker Cloudflare cache control = %q", cloudflareCacheControl)
			}
		})
	}
}

func TestFrontendVersionIsExposedAndInjected(t *testing.T) {
	frontend := fstest.MapFS{"index.html": {Data: []byte(`<meta name="wio-frontend-version" content="__WIO_FRONTEND_VERSION__">`)}}
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	api := &API{store: database, frontend: frontend, frontendHash: fingerprintFrontend(frontend)}
	if api.frontendHash == "" {
		t.Fatal("frontend fingerprint is empty")
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	api.serveFrontend(response, request)
	if !strings.Contains(response.Body.String(), `content="`+api.frontendHash+`"`) || strings.Contains(response.Body.String(), "__WIO_FRONTEND_VERSION__") {
		t.Fatalf("frontend version was not injected: %q", response.Body.String())
	}
	healthResponse := httptest.NewRecorder()
	api.health(healthResponse, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if healthResponse.Header().Get("Cache-Control") != "no-store" || !strings.Contains(healthResponse.Body.String(), `"frontend_version":"`+api.frontendHash+`"`) {
		t.Fatalf("frontend version was not exposed by health endpoint: headers=%v body=%s", healthResponse.Header(), healthResponse.Body.String())
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload)).WithContext(context.Background())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type testWriter struct{ t *testing.T }

func (writer testWriter) Write(value []byte) (int, error) {
	writer.t.Log(string(value))
	return len(value), nil
}
