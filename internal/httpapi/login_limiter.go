package httpapi

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

// Sign-in throttling policy.
//
// Failures are counted per real client address (see clientIP; IPv6 clients are
// grouped by /64 because a single host usually controls a whole /64). Once an
// address accumulates loginMaxFailures failures inside loginFailureWindow it is
// refused with 429 for loginBlockDuration. Attempts are reserved atomically
// before any password hashing so concurrent requests cannot all slip past the
// check.
//
// Failures are also counted per account, but the account counter never refuses
// a request: a hard per-username lock would let any anonymous attacker lock the
// only administrator out. Instead, after loginAccountSlowAfter failures inside
// loginFailureWindow every further attempt for that account is delayed by a
// growing, capped amount. Combined with the global hash semaphore this slows a
// distributed guessing attack while the administrator can still sign in from
// their own address (at worst a few seconds slower).
const (
	loginMaxFailures      = 5
	loginFailureWindow    = 15 * time.Minute
	loginBlockDuration    = 5 * time.Minute
	loginAccountSlowAfter = 10
	loginAccountBaseDelay = 250 * time.Millisecond
	loginAccountMaxDelay  = 3 * time.Second
	loginPruneInterval    = time.Minute
	// loginHashConcurrency bounds simultaneous Argon2id evaluations (64 MiB each).
	loginHashConcurrency = 2
	loginHashWait        = 20 * time.Second
)

type loginAttempt struct {
	Failures     int
	Pending      int
	WindowStart  time.Time
	BlockedUntil time.Time
}

type accountAttempt struct {
	Failures    int
	WindowStart time.Time
}

type loginLimiter struct {
	mu        sync.Mutex
	now       func() time.Time
	clients   map[string]*loginAttempt
	accounts  map[string]*accountAttempt
	lastPrune time.Time
	hashSlots chan struct{}
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		now:       time.Now,
		clients:   make(map[string]*loginAttempt),
		accounts:  make(map[string]*accountAttempt),
		hashSlots: make(chan struct{}, loginHashConcurrency),
	}
}

// limiterKey groups IPv6 clients by their /64 prefix.
func limiterKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if prefix, err := addr.Prefix(64); err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}

// reserve atomically checks the client's budget and, when allowed, reserves an
// attempt that must later be settled with fail, succeed, or release. It returns
// the remaining wait when the client is blocked.
func (l *loginLimiter) reserve(ip string) (time.Duration, bool) {
	key := limiterKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	attempt := l.clients[key]
	if attempt == nil {
		attempt = &loginAttempt{WindowStart: now}
		l.clients[key] = attempt
	}
	if now.Before(attempt.BlockedUntil) {
		return attempt.BlockedUntil.Sub(now), false
	}
	if now.Sub(attempt.WindowStart) >= loginFailureWindow {
		attempt.Failures = 0
		attempt.WindowStart = now
	}
	if attempt.Failures+attempt.Pending >= loginMaxFailures {
		// Concurrent requests already hold the remaining budget; ask the
		// client to retry once they settle.
		return time.Second, false
	}
	attempt.Pending++
	return 0, true
}

func (l *loginLimiter) settleLocked(key string) *loginAttempt {
	attempt := l.clients[key]
	if attempt == nil {
		attempt = &loginAttempt{WindowStart: l.now()}
		l.clients[key] = attempt
	}
	if attempt.Pending > 0 {
		attempt.Pending--
	}
	return attempt
}

// fail settles a reservation as a failed attempt.
func (l *loginLimiter) fail(ip string) {
	key := limiterKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	attempt := l.settleLocked(key)
	attempt.Failures++
	if attempt.Failures >= loginMaxFailures {
		attempt.BlockedUntil = now.Add(loginBlockDuration)
		attempt.Failures = 0
		attempt.WindowStart = now
	}
}

// succeed settles a reservation as a successful sign-in and clears the
// client's failure history.
func (l *loginLimiter) succeed(ip string) {
	key := limiterKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.settleLocked(key)
	attempt.Failures = 0
	attempt.BlockedUntil = time.Time{}
	if attempt.Pending == 0 {
		delete(l.clients, key)
	}
}

// release returns a reservation without counting it (malformed request,
// server-side error, or the request gave up waiting).
func (l *loginLimiter) release(ip string) {
	key := limiterKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.settleLocked(key)
	if attempt.Pending == 0 && attempt.Failures == 0 && !l.now().Before(attempt.BlockedUntil) {
		delete(l.clients, key)
	}
}

// accountDelay returns how long an attempt against the account should wait.
func (l *loginLimiter) accountDelay(account string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt := l.accounts[account]
	if attempt == nil {
		return 0
	}
	if l.now().Sub(attempt.WindowStart) >= loginFailureWindow {
		delete(l.accounts, account)
		return 0
	}
	excess := attempt.Failures - loginAccountSlowAfter
	if excess < 0 {
		return 0
	}
	delay := loginAccountBaseDelay
	for i := 0; i < excess && delay < loginAccountMaxDelay; i++ {
		delay *= 2
	}
	if delay > loginAccountMaxDelay {
		delay = loginAccountMaxDelay
	}
	return delay
}

func (l *loginLimiter) accountFailed(account string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	attempt := l.accounts[account]
	if attempt == nil || now.Sub(attempt.WindowStart) >= loginFailureWindow {
		attempt = &accountAttempt{WindowStart: now}
		l.accounts[account] = attempt
	}
	attempt.Failures++
}

func (l *loginLimiter) accountSucceeded(account string) {
	l.mu.Lock()
	delete(l.accounts, account)
	l.mu.Unlock()
}

// pruneLocked drops idle entries so the maps stay bounded by the number of
// clients seen within the failure window.
func (l *loginLimiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < loginPruneInterval {
		return
	}
	l.lastPrune = now
	for key, attempt := range l.clients {
		if attempt.Pending == 0 && !now.Before(attempt.BlockedUntil) &&
			(attempt.Failures == 0 || now.Sub(attempt.WindowStart) >= loginFailureWindow) {
			delete(l.clients, key)
		}
	}
	for key, attempt := range l.accounts {
		if now.Sub(attempt.WindowStart) >= loginFailureWindow {
			delete(l.accounts, key)
		}
	}
}

func (l *loginLimiter) size() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients), len(l.accounts)
}

// acquireHash waits for one of the bounded password-hashing slots.
func (l *loginLimiter) acquireHash(ctx context.Context) bool {
	timer := time.NewTimer(loginHashWait)
	defer timer.Stop()
	select {
	case l.hashSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (l *loginLimiter) releaseHash() { <-l.hashSlots }

// sleepContext waits for delay unless ctx ends first.
func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
