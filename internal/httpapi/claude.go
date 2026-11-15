package httpapi

import (
	"net/http"

	"github.com/wio-platform/wio/internal/session"
)

func (a *API) claudeThreads(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "list")
}
func (a *API) createClaudeThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "create")
}
func (a *API) claudeThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "detail")
}
func (a *API) updateClaudeThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "update")
}
func (a *API) deleteClaudeThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "delete")
}
func (a *API) claudeEvents(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "events")
}
func (a *API) startClaudeTurn(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "turns")
}
func (a *API) interruptClaudeTurn(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "interrupt")
}
func (a *API) confirmClaudeStopped(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "confirm-stopped")
}
