package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func mustTrustedProxies(t *testing.T, raw string) TrustedProxies {
	t.Helper()
	proxies, err := ParseTrustedProxies(raw)
	if err != nil {
		t.Fatal(err)
	}
	return proxies
}

func TestParseTrustedProxies(t *testing.T) {
	proxies := mustTrustedProxies(t, " 10.0.0.0/8, 127.0.0.1 ;fd00::/8\n::ffff:172.16.0.0/108 ")
	if got := proxies.String(); got != "10.0.0.0/8,127.0.0.1/32,fd00::/8,172.16.0.0/12" {
		t.Fatalf("unexpected parsed proxies %q", got)
	}
	if empty := mustTrustedProxies(t, ""); !empty.Empty() {
		t.Fatal("empty value should trust nothing")
	}
	for _, invalid := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.1:80", "::ffff:0.0.0.0/8"} {
		if _, err := ParseTrustedProxies(invalid); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestClientAddressFromForwardedFor(t *testing.T) {
	proxies := mustTrustedProxies(t, "10.0.0.0/8,127.0.0.1,fd00::/8")
	tests := []struct {
		name    string
		proxies TrustedProxies
		remote  string
		xff     []string
		want    string
	}{
		{name: "no proxy configured ignores header", proxies: TrustedProxies{}, remote: "10.0.0.5:1234", xff: []string{"203.0.113.9"}, want: "10.0.0.5"},
		{name: "untrusted peer cannot spoof", proxies: proxies, remote: "198.51.100.20:5555", xff: []string{"203.0.113.9"}, want: "198.51.100.20"},
		{name: "trusted peer forwards client", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{"203.0.113.9"}, want: "203.0.113.9"},
		{name: "client supplied prefix is ignored", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{"1.2.3.4, 203.0.113.9"}, want: "203.0.113.9"},
		{name: "client claiming a trusted address is still stopped at first untrusted hop", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{"10.9.9.9, 203.0.113.9"}, want: "203.0.113.9"},
		{name: "chain of trusted proxies is skipped", proxies: proxies, remote: "127.0.0.1:1234", xff: []string{"203.0.113.9, 10.1.1.1", "10.2.2.2"}, want: "203.0.113.9"},
		{name: "malformed hop stops the walk", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{"203.0.113.9, garbage"}, want: "10.0.0.5"},
		{name: "empty header falls back to peer", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{""}, want: "10.0.0.5"},
		{name: "all hops trusted returns leftmost", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{"10.3.3.3"}, want: "10.3.3.3"},
		{name: "forwarded entry with port", proxies: proxies, remote: "10.0.0.5:1234", xff: []string{"203.0.113.9:4444"}, want: "203.0.113.9"},
		{name: "ipv6 peer and client", proxies: proxies, remote: "[fd00::1]:443", xff: []string{"[2001:db8::7]"}, want: "2001:db8::7"},
		{name: "ipv4 mapped peer is normalized", proxies: proxies, remote: "[::ffff:10.0.0.5]:80", xff: []string{"::ffff:203.0.113.9"}, want: "203.0.113.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = test.remote
			for _, value := range test.xff {
				request.Header.Add("X-Forwarded-For", value)
			}
			if got := test.proxies.clientAddress(request); got != test.want {
				t.Fatalf("clientAddress = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClientIPMiddlewareFeedsHandlers(t *testing.T) {
	api := &API{trustedProxies: mustTrustedProxies(t, "10.0.0.0/8")}
	var seen string
	handler := api.resolveClientIP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = clientIP(r) }))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.5:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if seen != "203.0.113.9" {
		t.Fatalf("clientIP = %q", seen)
	}
}

func TestLoginLimiterReservesAtomically(t *testing.T) {
	limiter := newLoginLimiter()
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := limiter.reserve("203.0.113.9"); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != loginMaxFailures {
		t.Fatalf("expected %d concurrent reservations, got %d", loginMaxFailures, allowed)
	}
	for i := 0; i < allowed; i++ {
		limiter.fail("203.0.113.9")
	}
	if wait, ok := limiter.reserve("203.0.113.9"); ok || wait <= time.Minute {
		t.Fatalf("client should be blocked after %d failures (wait=%v ok=%v)", loginMaxFailures, wait, ok)
	}
	if _, ok := limiter.reserve("203.0.113.10"); !ok {
		t.Fatal("a different client must not be blocked")
	}
}

func TestLoginLimiterGroupsIPv6By64(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginMaxFailures; i++ {
		if _, ok := limiter.reserve("2001:db8:1:2::" + string(rune('a'+i))); !ok {
			t.Fatal("unexpected block")
		}
		limiter.fail("2001:db8:1:2::" + string(rune('a'+i)))
	}
	if _, ok := limiter.reserve("2001:db8:1:2::ffff"); ok {
		t.Fatal("same /64 should share the budget")
	}
	if _, ok := limiter.reserve("2001:db8:1:3::1"); !ok {
		t.Fatal("other /64 must not be blocked")
	}
}

func TestLoginLimiterPrunesStaleEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter()
	limiter.now = func() time.Time { return now }
	for i := 0; i < 200; i++ {
		ip := "198.51.100." + strconv.Itoa(i%250)
		if i >= 100 {
			ip = "203.0.113." + strconv.Itoa(i%250)
		}
		if _, ok := limiter.reserve(ip); !ok {
			t.Fatalf("unexpected block for %s", ip)
		}
		if i%2 == 0 {
			limiter.release(ip)
		} else {
			limiter.fail(ip)
		}
		limiter.accountFailed("user-" + strconv.Itoa(i))
	}
	// Released attempts leave nothing behind; failures are kept for the window.
	if clients, accounts := limiter.size(); clients != 100 || accounts != 200 {
		t.Fatalf("unexpected sizes before pruning: clients=%d accounts=%d", clients, accounts)
	}
	now = now.Add(loginFailureWindow + time.Second)
	if _, ok := limiter.reserve("192.0.2.1"); !ok {
		t.Fatal("unexpected block")
	}
	limiter.release("192.0.2.1")
	if clients, accounts := limiter.size(); clients != 0 || accounts != 0 {
		t.Fatalf("stale entries were not pruned: clients=%d accounts=%d", clients, accounts)
	}
	// Blocked clients are kept until the block expires, then pruned.
	for i := 0; i < loginMaxFailures; i++ {
		limiter.reserve("192.0.2.2")
		limiter.fail("192.0.2.2")
	}
	now = now.Add(loginPruneInterval)
	limiter.reserve("192.0.2.3")
	limiter.release("192.0.2.3")
	if clients, _ := limiter.size(); clients != 1 {
		t.Fatalf("blocked client should be retained, got %d entries", clients)
	}
	now = now.Add(loginBlockDuration + loginFailureWindow)
	limiter.reserve("192.0.2.3")
	limiter.release("192.0.2.3")
	if clients, _ := limiter.size(); clients != 0 {
		t.Fatalf("expired block should be pruned, got %d entries", clients)
	}
}

func TestLoginLimiterAccountDelaySlowsButNeverRefuses(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginAccountSlowAfter; i++ {
		limiter.accountFailed("admin")
	}
	if delay := limiter.accountDelay("admin"); delay != loginAccountBaseDelay {
		t.Fatalf("unexpected first delay %v", delay)
	}
	for i := 0; i < 100; i++ {
		limiter.accountFailed("admin")
	}
	if delay := limiter.accountDelay("admin"); delay != loginAccountMaxDelay {
		t.Fatalf("delay should be capped, got %v", delay)
	}
	if _, ok := limiter.reserve("203.0.113.50"); !ok {
		t.Fatal("account failures must not block a fresh client address")
	}
	limiter.accountSucceeded("admin")
	if delay := limiter.accountDelay("admin"); delay != 0 {
		t.Fatalf("success should clear the account delay, got %v", delay)
	}
}

func TestLoginHashSemaphoreIsBounded(t *testing.T) {
	limiter := newLoginLimiter()
	for i := 0; i < loginHashConcurrency; i++ {
		if !limiter.acquireHash(context.Background()) {
			t.Fatal("slot should be available")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if limiter.acquireHash(ctx) {
		t.Fatal("semaphore allowed more than the configured concurrency")
	}
	limiter.releaseHash()
	if !limiter.acquireHash(context.Background()) {
		t.Fatal("released slot should be reusable")
	}
}

func newLoginTestHandler(t *testing.T, trusted string) (http.Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	vault := security.DevVault()
	hub := realtime.New()
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	gateway := agentgateway.New(database, hub, vault, log)
	frontend, _ := fs.Sub(fstest.MapFS{"index.html": {Data: []byte("ok")}}, ".")
	handler := New(database, hub, gateway, vault, log, frontend, "http://localhost", true, WithTrustedProxies(mustTrustedProxies(t, trusted)))
	setup := requestJSON(t, handler, http.MethodPost, "/api/setup", map[string]string{"username": "admin", "auth_mode": store.AuthModePassword, "password": "correct-horse-battery"}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", setup.Code, setup.Body.String())
	}
	return handler, database
}

func loginFrom(handler http.Handler, remote, forwarded, username, password string) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remote
	if forwarded != "" {
		request.Header.Set("X-Forwarded-For", forwarded)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestLoginLockoutIsPerRealClientBehindProxy(t *testing.T) {
	handler, database := newLoginTestHandler(t, "172.16.0.0/12")
	const proxy = "172.18.0.3:40000"
	for i := 0; i < loginMaxFailures; i++ {
		// The attacker also tries to impersonate the administrator's address
		// by prefixing it; only the hop appended by the proxy counts.
		if response := loginFrom(handler, proxy, "198.51.100.8, 203.0.113.66", "admin", "wrong-password-123"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d: %s", i, response.Code, response.Body.String())
		}
	}
	if response := loginFrom(handler, proxy, "203.0.113.66", "admin", "correct-horse-battery"); response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("attacker should be blocked, got %d", response.Code)
	}
	// Unknown usernames count against the client as well.
	if response := loginFrom(handler, proxy, "203.0.113.66", "nobody", "whatever-password"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("attacker should stay blocked, got %d", response.Code)
	}
	response := loginFrom(handler, proxy, "198.51.100.8", "admin", "correct-horse-battery")
	if response.Code != http.StatusOK {
		t.Fatalf("administrator was locked out by another client: %d %s", response.Code, response.Body.String())
	}
	var ip string
	if err := database.DB.Get(&ip, database.Q("SELECT ip_address FROM audit_log WHERE action='auth.login'")); err != nil {
		t.Fatal(err)
	}
	if ip != "198.51.100.8" {
		t.Fatalf("audit recorded %q instead of the forwarded client address", ip)
	}
}

func TestLoginUnknownUserCountsAsFailure(t *testing.T) {
	handler, _ := newLoginTestHandler(t, "")
	for i := 0; i < loginMaxFailures; i++ {
		if response := loginFrom(handler, "203.0.113.70:1000", "", "nobody", "wrong-password-123"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d", i, response.Code)
		}
	}
	if response := loginFrom(handler, "203.0.113.70:1000", "", "admin", "correct-horse-battery"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected block, got %d", response.Code)
	}
	// Without trusted proxies, a forged header does not move the attacker to a
	// fresh bucket.
	if response := loginFrom(handler, "203.0.113.70:1000", "198.51.100.1", "admin", "correct-horse-battery"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("forged X-Forwarded-For bypassed the limiter: %d", response.Code)
	}
}

func TestParallelLoginAttemptsAreLimited(t *testing.T) {
	handler, _ := newLoginTestHandler(t, "")
	const attempts = 12
	codes := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- loginFrom(handler, "203.0.113.80:1000", "", "admin", "wrong-password-123").Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusUnauthorized] != loginMaxFailures || counts[http.StatusTooManyRequests] != attempts-loginMaxFailures {
		t.Fatalf("parallel attempts were not limited: %#v", counts)
	}
}

func TestDummyPasswordHashIsWellFormed(t *testing.T) {
	parts := strings.Split(dummyPasswordHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		t.Fatalf("unexpected dummy hash layout %q", dummyPasswordHash)
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	hash, hashErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || hashErr != nil || len(salt) != 16 || len(hash) != 32 {
		t.Fatalf("dummy hash would short-circuit verification: %v %v", saltErr, hashErr)
	}
	if security.VerifyPassword(dummyPasswordHash, "correct-horse-battery") {
		t.Fatal("dummy hash must never verify")
	}
}
