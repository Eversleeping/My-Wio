// Package credentialproxy keeps upstream authentication in the trusted Agent.
// A Lease only accepts its own temporary credential and fixed inference routes.
package credentialproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Credential struct {
	Header string `json:"header"`
	Value  string `json:"value"`
}

type Config struct {
	Provider             string       `json:"provider"`
	Native               bool         `json:"native,omitempty"`
	AccountID            string       `json:"account_id,omitempty"`
	Project              string       `json:"project,omitempty"`
	Upstream             string       `json:"upstream"`
	Credentials          []Credential `json:"credentials"`
	Secrets              []string     `json:"secrets,omitempty"`
	RequestReadTimeoutMS int          `json:"request_read_timeout_ms,omitempty"`
	ExpiresAt            time.Time    `json:"-"`
}

type Lease struct {
	config    Config
	target    *url.URL
	patterns  [][]byte
	token     string
	address   string
	server    *http.Server
	transport *http.Transport
	lifetime  context.Context
	revoke    context.CancelFunc
	closed    atomic.Bool
	stopOnce  sync.Once
	slots     chan struct{}
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}

type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

// Start creates an endpoint whose lifetime belongs to one CLI process. Native
// credentials with a finite lifetime are revoked at expiry; refresh must occur
// in the trusted account login, never inside the isolated process.
func Start(c Config) (*Lease, error) {
	target, err := url.Parse(c.Upstream)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Scheme != "https" && target.Scheme != "http" {
		return nil, errors.New("invalid fixed upstream")
	}
	if c.Provider != "codex" && c.Provider != "claude" && c.Provider != "gemini" || len(c.Credentials) == 0 {
		return nil, errors.New("unsupported credential mode")
	}
	if c.AccountID != "" && (!c.Native || c.Provider != "codex" || len(c.AccountID) > 200 || strings.ContainsAny(c.AccountID, "\x00\r\n")) {
		return nil, errors.New("unsupported native account metadata")
	}
	if c.Native && c.Provider == "gemini" && !regexp.MustCompile(`^[a-z][a-z0-9-]{4,62}[a-z0-9]$`).MatchString(c.Project) {
		return nil, errors.New("a fixed native Google project is required")
	}
	if c.Project != "" && (!c.Native || c.Provider != "gemini") {
		return nil, errors.New("unsupported native project metadata")
	}
	if !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(time.Now()) {
		return nil, errors.New("upstream authentication has expired; refresh the trusted login")
	}
	readTimeout := 30 * time.Second
	if c.RequestReadTimeoutMS != 0 {
		if c.RequestReadTimeoutMS < 100 || c.RequestReadTimeoutMS > 30000 {
			return nil, errors.New("unsupported input deadline")
		}
		readTimeout = time.Duration(c.RequestReadTimeoutMS) * time.Millisecond
	}
	c.Credentials = append([]Credential(nil), c.Credentials...)
	c.Secrets = append([]string(nil), c.Secrets...)
	patterns := make([][]byte, 0)
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		patterns = append(patterns, []byte(s))
	}
	secrets := append([]string(nil), c.Secrets...)
	for _, entry := range c.Credentials {
		if entry.Header != "Authorization" && entry.Header != "x-api-key" && entry.Header != "x-goog-api-key" || entry.Value == "" || strings.ContainsAny(entry.Value, "\x00\r\n") {
			return nil, errors.New("unsupported upstream authentication")
		}
		secrets = append(secrets, entry.Value, strings.TrimPrefix(entry.Value, "Bearer "))
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		add(secret)
		add(url.QueryEscape(secret))
		add(url.PathEscape(secret))
		encoded, _ := json.Marshal(secret)
		add(string(encoded[1 : len(encoded)-1]))
		add(base64.StdEncoding.EncodeToString([]byte(secret)))
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, errors.New("cannot create session authentication")
	}
	sessionToken := base64.RawURLEncoding.EncodeToString(token[:])
	if c.Native && c.Provider == "codex" {
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
		payload, _ := json.Marshal(map[string]any{"iss": "https://auth.openai.com", "sub": "wio-session", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": c.AccountID, "chatgpt_plan_type": "unknown", "chatgpt_user_id": "wio-session"}})
		sessionToken = base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + sessionToken
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot open session endpoint")
	}
	lifetime, revoke := context.WithCancel(context.Background())
	b := &Lease{config: c, target: target, patterns: patterns, token: sessionToken, address: listener.Addr().String(), lifetime: lifetime, revoke: revoke, slots: make(chan struct{}, 16)}
	b.transport = http.DefaultTransport.(*http.Transport).Clone()
	b.transport.ResponseHeaderTimeout = 30 * time.Second
	b.transport.DisableCompression = true
	b.transport.MaxConnsPerHost = 16
	b.transport.MaxIdleConnsPerHost = 16
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: readTimeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = b.server.Serve(&limitedListener{Listener: listener, slots: make(chan struct{}, 64)}) }()
	if !c.ExpiresAt.IsZero() {
		go func() {
			timer := time.NewTimer(time.Until(c.ExpiresAt))
			defer timer.Stop()
			select {
			case <-timer.C:
				b.stop()
			case <-b.lifetime.Done():
			}
		}()
	}
	return b, nil
}

// Endpoint and Token contain only a temporary inference credential.
func (b *Lease) Endpoint() string { return "http://" + b.address }
func (b *Lease) Token() string    { return b.token }

// Close revokes new requests, pending HTTP streams and native WebSockets.
func (b *Lease) Close() { b.stop() }

func (b *Lease) stop() {
	b.stopOnce.Do(func() {
		b.closed.Store(true)
		b.revoke()
		_ = b.server.Close()
		b.transport.CloseIdleConnections()
	})
}

var geminiPath = regexp.MustCompile(`^/(v1|v1beta)/models(/[A-Za-z0-9._-]+(:generateContent|:streamGenerateContent|:countTokens)?)?$`)

func allowedPath(provider, path string, method string) bool {
	if pathpkg.Clean(path) != path {
		return false
	}
	if method != http.MethodGet && method != http.MethodPost {
		return false
	}
	switch provider {
	case "codex":
		return method == http.MethodPost && (path == "/responses" || path == "/v1/responses") || method == http.MethodGet && (path == "/models" || path == "/v1/models")
	case "claude":
		return method == http.MethodPost && (path == "/v1/messages" || path == "/v1/messages/count_tokens") || method == http.MethodGet && path == "/v1/models"
	case "gemini":
		if !geminiPath.MatchString(path) {
			return false
		}
		return method == http.MethodPost && strings.Contains(path, ":") || method == http.MethodGet && !strings.Contains(path, ":")
	}
	return false
}

var geminiNativePath = regexp.MustCompile(`^/v1internal:(loadCodeAssist|fetchAdminControls|retrieveUserQuota|countTokens|generateContent|streamGenerateContent|recordConversation|recordCodeAssistMetrics)$`)

func (b *Lease) allowedPath(path, method string) bool {
	if b.config.Native && b.config.Provider == "gemini" {
		return method == http.MethodPost && pathpkg.Clean(path) == path && geminiNativePath.MatchString(path)
	}
	return allowedPath(b.config.Provider, path, method)
}

func inferenceQuery(provider string, raw string) (url.Values, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return nil, errors.New("invalid query")
	}
	for name, values := range query {
		switch strings.ToLower(name) {
		case "key", "api_key", "apikey", "access_token", "token":
			query.Del(name)
			continue
		}
		if len(values) != 1 {
			return nil, errors.New("ambiguous query")
		}
		value := values[0]
		switch {
		case provider == "claude" && name == "beta" && (value == "true" || value == "false"):
		case provider == "gemini" && name == "alt" && (value == "sse" || value == "json"):
		case provider == "gemini" && name == "pageSize":
			size, err := strconv.Atoi(value)
			if err != nil || size < 1 || size > 1000 {
				return nil, errors.New("invalid pagination")
			}
		case provider == "gemini" && name == "pageToken" && len(value) > 0 && len(value) <= 4096:
		default:
			return nil, errors.New("unsupported query")
		}
	}
	return query, nil
}

func (b *Lease) authorized(req *http.Request) bool {
	var presented string
	if b.config.Native {
		presented = strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		return subtle.ConstantTimeCompare([]byte(presented), []byte(b.token)) == 1
	}
	switch b.config.Provider {
	case "codex":
		presented = strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	case "claude":
		presented = req.Header.Get("x-api-key")
	case "gemini":
		presented = req.Header.Get("x-goog-api-key")
		if presented == "" {
			presented = req.URL.Query().Get("key")
		}
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(b.token)) == 1
}

func (b *Lease) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || req.Host != b.address || b.closed.Load() || !b.authorized(req) {
		http.Error(w, "session authentication rejected", http.StatusForbidden)
		return
	}
	socket := b.config.Native && b.config.Provider == "codex" && req.Method == http.MethodGet && (req.URL.Path == "/responses" || req.URL.Path == "/v1/responses") && websocket.IsWebSocketUpgrade(req)
	if req.URL.IsAbs() || req.URL.RawPath != "" || (!b.allowedPath(req.URL.Path, req.Method) && !socket) || req.Header.Get("Upgrade") != "" && !socket {
		http.Error(w, "unsupported inference route", http.StatusBadRequest)
		return
	}
	if req.ContentLength > 32<<20 || len(req.RequestURI) > 8<<10 {
		http.Error(w, "inference input too large", http.StatusRequestEntityTooLarge)
		return
	}
	rawQuery := req.URL.RawQuery
	clientVersion := ""
	if b.config.Native && b.config.Provider == "codex" {
		parsed, queryErr := url.ParseQuery(rawQuery)
		if queryErr != nil || len(parsed["client_version"]) > 1 {
			http.Error(w, "unsupported native query", http.StatusBadRequest)
			return
		}
		clientVersion = parsed.Get("client_version")
		if clientVersion != "" && !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(clientVersion) {
			http.Error(w, "unsupported native client version", http.StatusBadRequest)
			return
		}
		parsed.Del("client_version")
		rawQuery = parsed.Encode()
	}
	query, queryErr := inferenceQuery(b.config.Provider, rawQuery)
	if queryErr != nil {
		http.Error(w, "unsupported inference query", http.StatusBadRequest)
		return
	}
	if clientVersion != "" {
		query.Set("client_version", clientVersion)
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	default:
		http.Error(w, "session concurrency limit", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	release := context.AfterFunc(b.lifetime, cancel)
	defer release()
	defer cancel()
	out := req.Clone(ctx)
	copyURL := *b.target
	base := strings.TrimRight(copyURL.Path, "/")
	path := req.URL.Path
	// A base URL ending in /v1 already owns that segment.
	for _, version := range []string{"/v1beta", "/v1"} {
		if strings.HasSuffix(base, version) && strings.HasPrefix(path, version+"/") {
			path = strings.TrimPrefix(path, version)
			break
		}
	}
	copyURL.Path, copyURL.RawPath = base+path, ""
	copyURL.RawQuery = query.Encode()
	out.URL, out.Host, out.RequestURI = &copyURL, b.target.Host, ""
	out.Header = make(http.Header)
	for name, values := range req.Header {
		switch strings.ToLower(name) {
		case "content-type", "accept", "user-agent", "anthropic-version", "anthropic-beta", "openai-beta":
			out.Header[name] = append([]string(nil), values...)
		}
	}
	out.Header.Set("Accept-Encoding", "identity")
	for _, auth := range b.config.Credentials {
		out.Header.Set(auth.Header, auth.Value)
	}
	if b.config.Native && b.config.Provider == "codex" && b.config.AccountID != "" {
		out.Header.Set("ChatGPT-Account-Id", b.config.AccountID)
	}
	if socket {
		b.serveSocket(ctx, w, req, out.URL, out.Header)
		return
	}
	out.Body = http.MaxBytesReader(w, req.Body, 32<<20)
	if b.config.Native && b.config.Provider == "gemini" {
		body, err := b.bindGoogleProject(out.Body, req.URL.Path)
		if err != nil {
			http.Error(w, "unsupported native project request", http.StatusBadRequest)
			return
		}
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		out.TransferEncoding = nil
	}
	response, err := b.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "inference upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		http.Error(w, "unsupported upstream response", http.StatusBadGateway)
		return
	}
	source := io.Reader(response.Body)
	encodings := response.Header.Values("Content-Encoding")
	if len(encodings) > 0 {
		if len(encodings) != 1 {
			http.Error(w, "unsupported upstream encoding", http.StatusBadGateway)
			return
		}
		switch strings.ToLower(strings.TrimSpace(encodings[0])) {
		case "identity":
		case "gzip":
			unzip, err := gzip.NewReader(response.Body)
			if err != nil {
				http.Error(w, "invalid upstream encoding", http.StatusBadGateway)
				return
			}
			defer unzip.Close()
			source = unzip
		default:
			http.Error(w, "unsupported upstream encoding", http.StatusBadGateway)
			return
		}
	}
	for name, values := range response.Header {
		lower := strings.ToLower(name)
		if lower != "content-type" && lower != "retry-after" && lower != "x-request-id" && !strings.HasPrefix(lower, "x-ratelimit-") && !strings.HasPrefix(lower, "anthropic-ratelimit-") {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, b.redact(value))
		}
	}
	w.WriteHeader(response.StatusCode)
	buffer := make([]byte, 32<<10)
	reader := &redactor{source: source, patterns: b.patterns}
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *Lease) redact(value string) string {
	for _, pattern := range b.patterns {
		value = strings.ReplaceAll(value, string(pattern), "[redacted]")
	}
	return value
}

// Redact full matches and retain only a suffix that can still become a secret.
// Non-secret SSE events can therefore flush immediately, including at EOF.
type redactor struct {
	source   io.Reader
	patterns [][]byte
	pending  []byte
	ready    []byte
	terminal error
}

func (r *redactor) process(eof bool) {
	data := r.pending
	r.pending = nil
	for len(data) > 0 {
		position, width := -1, 0
		for _, pattern := range r.patterns {
			if found := bytes.Index(data, pattern); found >= 0 && (position < 0 || found < position || found == position && len(pattern) > width) {
				position, width = found, len(pattern)
			}
		}
		if position >= 0 {
			r.ready = append(r.ready, data[:position]...)
			r.ready = append(r.ready, "[redacted]"...)
			data = data[position+width:]
			continue
		}
		tail := 0
		if !eof {
			for _, pattern := range r.patterns {
				for length := min(len(data), len(pattern)-1); length > tail; length-- {
					if bytes.Equal(data[len(data)-length:], pattern[:length]) {
						tail = length
						break
					}
				}
			}
		}
		r.ready = append(r.ready, data[:len(data)-tail]...)
		r.pending = append(r.pending, data[len(data)-tail:]...)
		return
	}
}

func (r *redactor) Read(p []byte) (int, error) {
	for len(r.ready) == 0 && r.terminal == nil {
		var buffer [4096]byte
		n, err := r.source.Read(buffer[:])
		r.pending = append(r.pending, buffer[:n]...)
		r.process(err != nil)
		r.terminal = err
	}
	n := copy(p, r.ready)
	r.ready = r.ready[n:]
	if len(r.ready) == 0 {
		return n, r.terminal
	}
	return n, nil
}
