package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/agentupdate"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
)

const sessionCookie = "wio_session"

type API struct {
	store         *store.Store
	hub           *realtime.Hub
	gateway       *agentgateway.Gateway
	vault         *security.Vault
	log           *slog.Logger
	frontend      fs.FS
	frontendHash  string
	bootstrapper  serverBootstrapper
	agentUpdates  *agentupdate.Store
	codexReleases *codexcli.ReleaseChecker
	publicURL     string
	secureCookie  bool
	setupMu       sync.Mutex
	loginMu       sync.Mutex
	bootstrapMu   sync.Mutex
	login         map[string]*loginAttempt
}

type loginAttempt struct {
	Failures int
	Blocked  time.Time
}

type sessionContextKey struct{}

func New(s *store.Store, hub *realtime.Hub, gateway *agentgateway.Gateway, vault *security.Vault, log *slog.Logger, frontend fs.FS, publicURL string, devInsecure bool) http.Handler {
	assetDir := os.Getenv("WIO_AGENT_ASSET_DIR")
	api := &API{
		store:         s,
		hub:           hub,
		gateway:       gateway,
		vault:         vault,
		log:           log,
		frontend:      frontend,
		frontendHash:  fingerprintFrontend(frontend),
		bootstrapper:  sshbootstrap.New(assetDir),
		agentUpdates:  agentupdate.New(assetDir),
		codexReleases: codexcli.NewReleaseChecker(nil, ""),
		publicURL:     strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		secureCookie:  strings.HasPrefix(strings.ToLower(publicURL), "https://") && !devInsecure,
		login:         make(map[string]*loginAttempt),
	}
	router := chi.NewRouter()
	router.Use(api.recoverer, api.securityHeaders)
	router.Route("/api", func(r chi.Router) {
		r.Get("/health", api.health)
		r.Get("/setup/status", api.setupStatus)
		r.Post("/setup", api.setup)
		r.Post("/auth/login", api.loginHandler)
		r.Post("/agent/enroll", api.enrollAgent)
		r.Get("/agent/update-package/{architecture}", api.downloadAgentUpdate)
		r.Group(func(private chi.Router) {
			private.Use(api.authenticate, api.requireCSRF)
			private.Get("/auth/session", api.session)
			private.Post("/auth/logout", api.logout)
			private.Get("/summary", api.summary)
			private.Get("/servers", api.servers)
			private.Patch("/servers/{serverID}", api.updateServer)
			private.Post("/servers/{serverID}/credential-profiles", api.updateServerCredentialProfiles)
			private.Post("/servers/{serverID}/agent-update", api.updateAgent)
			private.Post("/servers/{serverID}/codex-update", api.updateCodexCLI)
			private.Post("/servers/enrollments", api.createEnrollment)
			private.Post("/servers/ssh/probe", api.probeServerSSH)
			private.Post("/servers/ssh/bootstrap", api.bootstrapServerSSH)
			private.Post("/servers/ssh/bootstrap-stream", api.streamBootstrapServerSSH)
			private.Delete("/servers/{serverID}", api.revokeServer)
			private.Get("/servers/{serverID}/metrics", api.metrics)
			private.Get("/projects", api.projects)
			private.Patch("/projects/{projectID}", api.updateProject)
			private.Get("/workspaces", api.workspaces)
			private.Get("/workspaces/{workspaceID}/files", api.workspaceFiles)
			private.Post("/workspaces/{workspaceID}/files/refresh", api.refreshWorkspaceFiles)
			private.Get("/workspaces/{workspaceID}/file-preview", api.workspaceFilePreview)
			private.Post("/workspaces/{workspaceID}/file-preview", api.requestWorkspaceFilePreview)
			private.Post("/projects/import", api.importProject)
			private.Post("/projects/discover", api.discoverProjects)
			private.Post("/projects/{projectID}/retry-import", api.retryProjectImport)
			private.Delete("/projects/{projectID}", api.deleteProject)
			private.Get("/threads", api.threads)
			private.Post("/threads", api.createThread)
			private.Patch("/threads/{threadID}", api.updateThread)
			private.Delete("/threads/{threadID}", api.deleteThread)
			private.Get("/threads/{threadID}/events", api.threadEvents)
			private.Post("/threads/{threadID}/turns", api.startTurn)
			private.Post("/threads/{threadID}/events/{eventID}/rewrite", api.rewriteTurn)
			private.Post("/threads/{threadID}/interrupt", api.interruptTurn)
			private.Get("/approvals", api.approvals)
			private.Post("/approvals/{approvalID}/decision", api.decideApproval)
			private.Get("/secret-sets", api.secretSets)
			private.Post("/secret-sets", api.upsertSecretSet)
			private.Delete("/secret-sets/{secretID}", api.deleteSecretSet)
			private.Get("/credential-profiles", api.credentialProfiles)
			private.Post("/credential-profiles", api.saveCredentialProfile)
			private.Delete("/credential-profiles/{profileID}", api.deleteCredentialProfile)
			private.Get("/settings/codex-cli", api.codexCLISettings)
			private.Post("/settings/codex-cli/check-updates", api.checkCodexCLIUpdates)
			private.Post("/settings/codex-cli/select-version", api.selectCodexCLIVersion)
			private.Get("/deployment-targets", api.deploymentTargets)
			private.Post("/deployment-targets", api.createDeploymentTarget)
			private.Get("/deployments", api.deployments)
			private.Post("/deployment-targets/{targetID}/deploy", api.startDeployment)
			private.Post("/deployment-targets/{targetID}/rollback", api.rollbackDeployment)
			private.Get("/alerts", api.alerts)
			private.Post("/alerts/{alertID}/acknowledge", api.acknowledgeAlert)
			private.Get("/audit", api.auditLog)
			private.Get("/ws", api.websocket)
		})
	})
	router.NotFound(api.serveFrontend)
	return router
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.log.Error("http handler panic", "error", recovered, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.DB.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC(), "version": buildinfo.Version, "frontend_version": a.frontendHash})
}

func fingerprintFrontend(frontend fs.FS) string {
	if frontend == nil {
		return ""
	}
	data, err := fs.ReadFile(frontend, "index.html")
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func (a *API) setupStatus(w http.ResponseWriter, r *http.Request) {
	configured, err := a.store.HasUser(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read setup state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"configured": configured})
}

func (a *API) setup(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	configured, err := a.store.HasUser(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read setup state")
		return
	}
	if configured {
		writeError(w, http.StatusConflict, "administrator is already configured")
		return
	}
	var input struct {
		Username string `json:"username"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if len(input.Username) < 3 || len(input.Username) > 64 {
		writeError(w, http.StatusBadRequest, "username must contain 3 to 64 characters")
		return
	}
	// Keep the legacy non-null database field populated with a random, unusable
	// value. Authentication is intentionally TOTP-only.
	passwordToken, err := security.RandomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate administrator credential")
		return
	}
	passwordHash, err := security.HashPassword(passwordToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Wio", AccountName: input.Username})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate TOTP secret")
		return
	}
	encryptedSecret, err := a.vault.Encrypt(key.Secret())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect TOTP secret")
		return
	}
	codes, hashes, err := security.GenerateRecoveryCodes(10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate recovery codes")
		return
	}
	recovery, _ := json.Marshal(hashes)
	user := store.User{ID: store.NewID(), Username: input.Username, PasswordHash: passwordHash, TOTPSecret: encryptedSecret, RecoveryHashes: string(recovery)}
	if err := a.store.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create administrator")
		return
	}
	_ = a.store.Audit(r.Context(), user.ID, "setup.complete", "user", user.ID, map[string]string{"username": input.Username}, clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"username": input.Username, "totp_uri": key.URL(), "totp_secret": key.Secret(), "recovery_codes": codes})
}

func (a *API) loginHandler(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait := a.loginBlocked(ip); wait > 0 {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many failed sign-in attempts")
		return
	}
	var input struct {
		Username string `json:"username"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	user, err := a.store.UserByName(r.Context(), strings.TrimSpace(input.Username))
	if err != nil {
		a.loginFailed(ip)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(input.Code), " ", ""))
	var secret string
	if err := a.vault.Decrypt(user.TOTPSecret, &secret); err != nil {
		writeError(w, http.StatusInternalServerError, "could not read TOTP secret")
		return
	}
	valid := totp.Validate(code, secret)
	usedRecovery := false
	if !valid {
		var hashes []string
		_ = json.Unmarshal([]byte(user.RecoveryHashes), &hashes)
		needle := security.HashRecovery(code)
		for index, hash := range hashes {
			if hash == needle {
				hashes = append(hashes[:index], hashes[index+1:]...)
				updated, _ := json.Marshal(hashes)
				_, err = a.store.DB.ExecContext(r.Context(), a.store.Q("UPDATE users SET recovery_hashes=? WHERE id=?"), string(updated), user.ID)
				valid = err == nil
				usedRecovery = true
				break
			}
		}
	}
	if !valid {
		a.loginFailed(ip)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	a.loginSucceeded(ip)
	token, err := security.RandomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	csrf, _ := security.RandomToken(24)
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	if err := a.store.CreateSession(r.Context(), user.ID, store.HashToken(token), csrf, expires); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int((7 * 24 * time.Hour).Seconds())})
	_ = a.store.Audit(r.Context(), user.ID, "auth.login", "session", "", map[string]bool{"recovery_code": usedRecovery}, ip)
	writeJSON(w, http.StatusOK, map[string]any{"username": user.Username, "csrf_token": csrf, "expires_at": expires})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		session, err := a.store.SessionByToken(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			session := currentSession(r)
			if r.Header.Get("X-CSRF-Token") == "" || r.Header.Get("X-CSRF-Token") != session.CSRFToken {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	session := currentSession(r)
	writeJSON(w, http.StatusOK, map[string]any{"username": session.Username, "csrf_token": session.CSRFToken, "expires_at": session.ExpiresAt})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		EnrollmentToken string `json:"enrollment_token"`
		Hostname        string `json:"hostname"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.EnrollmentToken == "" || input.Hostname == "" {
		writeError(w, http.StatusBadRequest, "enrollment_token and hostname are required")
		return
	}
	enrollment, err := a.store.ConsumeEnrollment(r.Context(), input.EnrollmentToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired enrollment token")
		return
	}
	agentToken, err := security.RandomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create agent token")
		return
	}
	server, err := a.store.EnrollServer(r.Context(), enrollment, input.Hostname, agentToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not enroll server")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server_id": server.ID, "agent_token": agentToken, "scan_roots": json.RawMessage(enrollment.ScanRoots)})
}

func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, r.Host)
	}}
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(1024)
	connection.SetReadDeadline(time.Now().Add(70 * time.Second))
	connection.SetPongHandler(func(string) error {
		connection.SetReadDeadline(time.Now().Add(70 * time.Second))
		return nil
	})
	go func() {
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}()
	id, events := a.hub.Subscribe()
	defer a.hub.Unsubscribe(id)
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || connection.WriteJSON(event) != nil {
				return
			}
		case <-ping.C:
			if connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (a *API) serveFrontend(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || a.frontend == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(a.frontend, name)
	if err != nil && name == "sw.js" {
		// Keep existing PWA installations updatable after versioning the worker filename.
		data, err = fs.ReadFile(a.frontend, "sw-v2.js")
	}
	if err != nil {
		data, err = fs.ReadFile(a.frontend, "index.html")
		name = "index.html"
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "frontend is not built")
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if name == "index.html" && a.frontendHash != "" {
		data = []byte(strings.ReplaceAll(string(data), "__WIO_FRONTEND_VERSION__", a.frontendHash))
	}
	isServiceWorker := name == "sw.js" || strings.HasPrefix(name, "sw-") &&
		(strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".js.map"))
	if name == "index.html" || name == "manifest.webmanifest" || isServiceWorker {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("CDN-Cache-Control", "no-store")
		w.Header().Set("Cloudflare-CDN-Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	_, _ = w.Write(data)
}

func (a *API) loginBlocked(ip string) time.Duration {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	attempt := a.login[ip]
	if attempt == nil || time.Now().After(attempt.Blocked) {
		return 0
	}
	return time.Until(attempt.Blocked)
}

func (a *API) loginFailed(ip string) {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	attempt := a.login[ip]
	if attempt == nil {
		attempt = &loginAttempt{}
		a.login[ip] = attempt
	}
	attempt.Failures++
	if attempt.Failures >= 5 {
		attempt.Blocked = time.Now().Add(5 * time.Minute)
		attempt.Failures = 0
	}
}

func (a *API) loginSucceeded(ip string) {
	a.loginMu.Lock()
	delete(a.login, ip)
	a.loginMu.Unlock()
}

func currentSession(r *http.Request) store.Session {
	return r.Context().Value(sessionContextKey{}).(store.Session)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	return decodeJSONLimit(w, r, out, 1<<20)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, out any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func databaseConflict(err error) bool {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}

func eventPayload(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

var _ = protocol.StreamEvent{}
