package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/agentupdate"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/gitprovider"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const sessionCookie = "wio_session"

// contentSecurityPolicy fits the Vite SPA: scripts, the service worker and the
// image-compression worker are same-origin bundles, and React style props plus
// third-party highlighters may write inline styles. Remote images are never
// loaded inline, so img-src stays limited to the app itself and pasted images.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

const (
	websocketWriteTimeout         = 10 * time.Second
	websocketPongWait             = 70 * time.Second
	websocketPingInterval         = 30 * time.Second
	websocketSessionCheckInterval = time.Minute
	websocketSessionCheckTimeout  = 5 * time.Second
)

type API struct {
	store          *store.Store
	hub            *realtime.Hub
	gateway        *agentgateway.Gateway
	vault          *security.Vault
	log            *slog.Logger
	frontend       fs.FS
	frontendHash   string
	bootstrapper   serverBootstrapper
	agentUpdates   *agentupdate.Store
	codexReleases  *codexcli.ReleaseChecker
	geminiReleases *codexcli.ReleaseChecker
	claudeReleases *codexcli.ReleaseChecker
	gitProviders   gitprovider.Creator
	publicURL      string
	secureCookie   bool
	trustedProxies TrustedProxies
	setupMu        sync.Mutex
	setupToken     string
	bootstrapMu    sync.Mutex
	login          *loginLimiter
	// wsSessionCheck is how often open WebSockets re-validate their session.
	wsSessionCheck time.Duration
}

type sessionContextKey struct{}

func New(s *store.Store, hub *realtime.Hub, gateway *agentgateway.Gateway, vault *security.Vault, log *slog.Logger, frontend fs.FS, publicURL string, devInsecure bool, options ...Option) http.Handler {
	assetDir := os.Getenv("WIO_AGENT_ASSET_DIR")
	api := &API{
		store:          s,
		hub:            hub,
		gateway:        gateway,
		vault:          vault,
		log:            log,
		frontend:       frontend,
		frontendHash:   fingerprintFrontend(frontend),
		bootstrapper:   sshbootstrap.New(assetDir),
		agentUpdates:   agentupdate.New(assetDir),
		codexReleases:  codexcli.NewReleaseChecker(nil, ""),
		geminiReleases: codexcli.NewTaggedReleaseChecker(nil, "https://api.github.com/repos/google-gemini/gemini-cli/releases", "v"),
		claudeReleases: codexcli.NewTaggedReleaseChecker(nil, "https://api.github.com/repos/anthropics/claude-code/releases", "v"),
		gitProviders:   gitprovider.Client{},
		publicURL:      strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		secureCookie:   strings.HasPrefix(strings.ToLower(publicURL), "https://") && !devInsecure,
		login:          newLoginLimiter(),
	}
	for _, option := range options {
		option(api)
	}
	api.initSetupToken()
	router := chi.NewRouter()
	router.Use(api.resolveClientIP, api.recoverer, api.securityHeaders)
	api.registerRoutes(router)
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
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
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
	status := map[string]any{"configured": configured}
	if configured {
		mode, modeErr := a.store.UserAuthMode(r.Context())
		if modeErr != nil {
			writeError(w, http.StatusInternalServerError, "could not read authentication mode")
			return
		}
		status["auth_mode"] = mode
	}
	writeJSON(w, http.StatusOK, status)
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
		SetupToken string `json:"setup_token"`
		Username   string `json:"username"`
		AuthMode   string `json:"auth_mode"`
		Password   string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !a.setupTokenMatches(input.SetupToken) {
		writeError(w, http.StatusForbidden, "setup requires a valid setup token; find it in the control-plane logs or set WIO_SETUP_TOKEN")
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if len(input.Username) < 3 || len(input.Username) > 64 {
		writeError(w, http.StatusBadRequest, "username must contain 3 to 64 characters")
		return
	}
	authMode := strings.TrimSpace(input.AuthMode)
	if authMode == "" {
		authMode = store.AuthModeTOTP
	}
	needsPassword := authMode == store.AuthModePassword || authMode == store.AuthModePasswordTOTP
	needsCode := authMode == store.AuthModeTOTP || authMode == store.AuthModePasswordTOTP
	if !needsPassword && !needsCode {
		writeError(w, http.StatusBadRequest, "unsupported authentication mode")
		return
	}
	passwordHash := ""
	if needsPassword {
		passwordHash, err = security.HashPassword(input.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		// Keep the legacy non-null database field populated with a random,
		// unusable value when password authentication is disabled.
		passwordToken, tokenErr := security.RandomToken(32)
		if tokenErr != nil {
			writeError(w, http.StatusInternalServerError, "could not generate administrator credential")
			return
		}
		passwordHash, err = security.HashPassword(passwordToken)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate administrator credential")
			return
		}
	}
	totpURI, totpSecret, recoveryCodes, recoveryHashes := "", "", []string(nil), []string(nil)
	if needsCode {
		key, keyErr := totp.Generate(totp.GenerateOpts{Issuer: "Wio", AccountName: input.Username})
		if keyErr != nil {
			writeError(w, http.StatusInternalServerError, "could not generate TOTP secret")
			return
		}
		encryptedSecret, encryptErr := a.vault.Encrypt(key.Secret())
		if encryptErr != nil {
			writeError(w, http.StatusInternalServerError, "could not protect TOTP secret")
			return
		}
		totpURI, totpSecret = key.URL(), key.Secret()
		var codesErr error
		recoveryCodes, recoveryHashes, codesErr = security.GenerateRecoveryCodes(10)
		if codesErr != nil {
			writeError(w, http.StatusInternalServerError, "could not generate recovery codes")
			return
		}
		recoveryJSON, _ := json.Marshal(recoveryHashes)
		user := store.User{ID: store.NewID(), Username: input.Username, AuthMode: authMode, PasswordHash: passwordHash, TOTPSecret: encryptedSecret, RecoveryHashes: string(recoveryJSON)}
		if err := a.store.CreateUser(r.Context(), user); err != nil {
			writeError(w, http.StatusInternalServerError, "could not create administrator")
			return
		}
		a.setupToken = ""
		_ = a.store.Audit(r.Context(), user.ID, "setup.complete", "user", user.ID, map[string]string{"username": input.Username, "auth_mode": authMode}, clientIP(r))
		writeJSON(w, http.StatusCreated, map[string]any{"username": input.Username, "auth_mode": authMode, "totp_uri": totpURI, "totp_secret": totpSecret, "recovery_codes": recoveryCodes})
		return
	}
	user := store.User{ID: store.NewID(), Username: input.Username, AuthMode: authMode, PasswordHash: passwordHash, RecoveryHashes: "[]"}
	if err := a.store.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create administrator")
		return
	}
	a.setupToken = ""
	_ = a.store.Audit(r.Context(), user.ID, "setup.complete", "user", user.ID, map[string]string{"username": input.Username, "auth_mode": authMode}, clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"username": input.Username, "auth_mode": authMode})
}

// dummyPasswordHash is verified when the requested account does not exist so
// unknown usernames cost the same Argon2id work as known ones. It is a
// well-formed encoding that no password can match in practice.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (a *API) loginHandler(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	// Reserve the attempt before any expensive work so parallel requests
	// cannot all pass the check before the first failure is recorded.
	if wait, ok := a.login.reserve(ip); !ok {
		retryAfter := int((wait + time.Second - 1) / time.Second)
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, http.StatusTooManyRequests, "too many failed sign-in attempts")
		return
	}
	settled := false
	settle := func(outcome func(string)) {
		if !settled {
			settled = true
			outcome(ip)
		}
	}
	defer settle(a.login.release)
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	user, err := a.store.UserByName(r.Context(), strings.TrimSpace(input.Username))
	userFound := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "could not read user")
		return
	}
	if userFound {
		if !sleepContext(r.Context(), a.login.accountDelay(user.ID)) {
			return
		}
	}
	if !a.login.acquireHash(r.Context()) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "sign-in is busy, retry shortly")
		return
	}
	hashHeld := true
	releaseHash := func() {
		if hashHeld {
			hashHeld = false
			a.login.releaseHash()
		}
	}
	defer releaseHash()
	if !userFound {
		_ = security.VerifyPassword(dummyPasswordHash, input.Password)
		releaseHash()
		settle(a.login.fail)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	authMode := user.AuthMode
	if authMode == "" {
		authMode = store.AuthModeTOTP
	}
	needsPassword := authMode == store.AuthModePassword || authMode == store.AuthModePasswordTOTP
	needsCode := authMode == store.AuthModeTOTP || authMode == store.AuthModePasswordTOTP
	if !needsPassword && !needsCode {
		releaseHash()
		settle(a.login.fail)
		a.login.accountFailed(user.ID)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	passwordValid := !needsPassword || security.VerifyPassword(user.PasswordHash, input.Password)
	valid := !needsCode
	usedRecovery := false
	if needsCode {
		code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(input.Code), " ", ""))
		var secret string
		if err := a.vault.Decrypt(user.TOTPSecret, &secret); err != nil {
			writeError(w, http.StatusInternalServerError, "could not read TOTP secret")
			return
		}
		counter, matched := security.MatchTOTP(code, secret, time.Now())
		// A code is spent only by an otherwise valid attempt, so a wrong
		// password cannot burn the time step or a recovery code.
		if matched && passwordValid {
			valid, err = a.store.AdvanceTOTPCounter(r.Context(), user.ID, counter)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not verify TOTP code")
				return
			}
		} else if !matched && passwordValid {
			valid, err = a.store.ConsumeRecoveryCode(r.Context(), user.ID, security.HashRecovery(code))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not verify recovery code")
				return
			}
			usedRecovery = valid
		}
	}
	releaseHash()
	if !passwordValid || !valid {
		settle(a.login.fail)
		a.login.accountFailed(user.ID)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	settle(a.login.succeed)
	a.login.accountSucceeded(user.ID)
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
	_ = a.store.Audit(r.Context(), user.ID, "auth.login", "session", "", map[string]any{"auth_mode": authMode, "recovery_code": usedRecovery}, ip)
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
	session := currentSession(r)
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
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
	// r.Context() stops tracking the client once the connection is hijacked, so
	// the reader goroutine is the only way to observe a disconnect.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	connection.SetReadLimit(1024)
	connection.SetReadDeadline(time.Now().Add(websocketPongWait))
	connection.SetPongHandler(func(string) error {
		connection.SetReadDeadline(time.Now().Add(websocketPongWait))
		return nil
	})
	go func() {
		defer cancel()
		// Closing also unblocks a write stuck on an unresponsive peer.
		defer connection.Close()
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}()
	id, events := a.hub.Subscribe()
	defer a.hub.Unsubscribe(id)
	ping := time.NewTicker(websocketPingInterval)
	defer ping.Stop()
	checkInterval := a.wsSessionCheck
	if checkInterval <= 0 {
		checkInterval = websocketSessionCheckInterval
	}
	sessionCheck := time.NewTicker(checkInterval)
	defer sessionCheck.Stop()
	expiry := time.NewTimer(time.Until(session.ExpiresAt))
	defer expiry.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			_ = connection.SetWriteDeadline(time.Now().Add(websocketWriteTimeout))
			if connection.WriteJSON(event) != nil {
				return
			}
		case <-ping.C:
			if connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		case <-sessionCheck.C:
			if !a.websocketSessionActive(ctx, cookie.Value) {
				closeWebsocketForSession(connection)
				return
			}
		case <-expiry.C:
			closeWebsocketForSession(connection)
			return
		case <-ctx.Done():
			return
		}
	}
}

// websocketSessionActive only reports false when the session is definitely gone
// (logged out or expired); a database hiccup must not disconnect every client.
func (a *API) websocketSessionActive(ctx context.Context, token string) bool {
	ctx, cancel := context.WithTimeout(ctx, websocketSessionCheckTimeout)
	defer cancel()
	_, err := a.store.SessionByToken(ctx, token)
	return !errors.Is(err, sql.ErrNoRows)
}

func closeWebsocketForSession(connection *websocket.Conn) {
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session ended"), time.Now().Add(time.Second))
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

const (
	serverAddressLimit       = 255
	serverConfigurationLimit = 4096
	serverNotesLimit         = 4096
	projectNameLimit         = 200
	projectDestinationLimit  = 1024
	gitBranchNameLimit       = 240
	threadTitleLimit         = 200
	defaultThreadsPageLimit  = 50
	maxThreadsPageLimit      = 100
	defaultAuditPageLimit    = 50
	maxAuditPageLimit        = 100
)

// errGitRemoteCredentials explains why a remote URL with embedded secrets is
// refused: remotes are stored and shown in plaintext, unlike vault entries.
var errGitRemoteCredentials = errors.New("Git remote URL must not contain credentials; remove the user:token@ part and save the token as a Git credential profile in the credentials vault (Settings > Credentials)")

// gitRemoteCredentialError rejects a password in any URL and any userinfo in an
// HTTP(S) URL, where a bare username is usually a token. SSH users such as
// ssh://git@host carry no secret and scp-style remotes have no password syntax.
// The authority is inspected by hand so URLs that url.Parse refuses (and that
// lenient callers may still accept) cannot smuggle credentials through.
func gitRemoteCredentialError(remote string) error {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(remote), "://")
	if !ok {
		return nil
	}
	authority := rest
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return nil
	}
	scheme = strings.ToLower(scheme)
	if strings.Contains(authority[:at], ":") || scheme == "http" || scheme == "https" {
		return errGitRemoteCredentials
	}
	return nil
}

func validGitRemote(remote string) bool {
	if gitRemoteCredentialError(remote) != nil {
		return false
	}
	if strings.HasPrefix(remote, "git@") || strings.HasPrefix(remote, "ssh://") {
		return true
	}
	parsed, err := url.Parse(remote)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func repositoryName(remote string) string {
	cleaned := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	cleaned = strings.TrimSuffix(cleaned, "/")
	name := filepath.Base(strings.ReplaceAll(cleaned, ":", "/"))
	if name == "." || name == "" {
		return "Imported project"
	}
	return name
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

var _ = sql.ErrNoRows

var _ = fmt.Sprintf
