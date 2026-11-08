package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"os"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/security"
)

// WithSetupToken fixes the token POST /api/setup requires, taking precedence
// over WIO_SETUP_TOKEN and token generation.
func WithSetupToken(token string) Option {
	return func(api *API) { api.setupToken = strings.TrimSpace(token) }
}

// WithWebSocketSessionCheckInterval changes how often open WebSockets
// re-validate their session.
func WithWebSocketSessionCheckInterval(interval time.Duration) Option {
	return func(api *API) { api.wsSessionCheck = interval }
}

// initSetupToken guards the first-run setup, which would otherwise hand the
// administrator account to whoever reaches a fresh deployment first. Without a
// token setup fails closed, so an unset value can never mean "no check".
func (a *API) initSetupToken() {
	if a.setupToken != "" {
		return
	}
	if configured := strings.TrimSpace(os.Getenv("WIO_SETUP_TOKEN")); configured != "" {
		a.setupToken = configured
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A failed lookup also generates a token: the database may be down only
	// briefly, and setup would otherwise stay locked until the next restart.
	if configured, err := a.store.HasUser(ctx); err == nil && configured {
		return
	}
	token, err := security.RandomToken(32)
	if err != nil {
		a.log.Error("could not generate a setup token; set WIO_SETUP_TOKEN to enable first-run setup", "error", err)
		return
	}
	a.setupToken = token
	a.log.Warn("FIRST-RUN SETUP TOKEN: no administrator exists yet. Enter this token on the setup page to create the administrator (POST /api/setup, field setup_token). It changes on every restart until setup completes and stops working once the administrator exists; set WIO_SETUP_TOKEN to choose your own.", "setup_token", token)
}

// setupTokenMatches compares digests so neither the content nor the length of
// the expected token is revealed by timing. The caller must hold setupMu.
func (a *API) setupTokenMatches(candidate string) bool {
	if a.setupToken == "" {
		return false
	}
	expected, provided := sha256.Sum256([]byte(a.setupToken)), sha256.Sum256([]byte(strings.TrimSpace(candidate)))
	return subtle.ConstantTimeCompare(expected[:], provided[:]) == 1
}
