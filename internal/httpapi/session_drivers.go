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

type sessionListReader interface {
	List(context.Context, session.Filter) (session.Page, error)
}

func (a *API) sessionReader() sessionListReader { return &sessionservice.Service{Store: a.store} }
