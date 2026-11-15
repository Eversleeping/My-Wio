package httpapi

import (
	"context"
	"net/http"

	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessionservice"
)

// sessionHTTPDriver adds transport-specific native validation and compatibility
// responses to the shared control-plane driver. Optional actions return nil.
type sessionHTTPDriver interface {
	session.Driver
	Handler(string) http.HandlerFunc
}

func (a *API) sessionDriver(engine string) (sessionHTTPDriver, error) {
	registry := sessionservice.NewDrivers(a.store, a.gateway.Wake, a.log)
	driver, err := registry.Driver(engine)
	if err != nil {
		return nil, err
	}
	factories := map[string]func(session.Driver) sessionHTTPDriver{
		session.Codex:  func(d session.Driver) sessionHTTPDriver { return &codexHTTPDriver{a, d} },
		session.Gemini: func(d session.Driver) sessionHTTPDriver { return &geminiHTTPDriver{a, d} },
		session.Claude: func(d session.Driver) sessionHTTPDriver { return &claudeHTTPDriver{a, d} },
	}
	return factories[engine](driver), nil
}
func (a *API) legacySession(w http.ResponseWriter, r *http.Request, engine, action string) {
	driver, err := a.sessionDriver(engine)
	if err != nil {
		writeError(w, 400, "invalid session engine")
		return
	}
	handler := driver.Handler(action)
	if handler == nil {
		writeUnsupportedSessionAction(w)
		return
	}
	handler(w, r)
}

type sessionListReader interface {
	List(context.Context, session.Filter) (session.Page, error)
}

func (a *API) sessionReader() sessionListReader { return &sessionservice.Service{Store: a.store} }
