package httpapi

import (
	"net/http"

	"github.com/wio-platform/wio/internal/session"
)

func (a *API) geminiThreads(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "list")
}
func (a *API) createGeminiThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "create")
}
func (a *API) geminiThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "detail")
}
func (a *API) updateGeminiThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "update")
}
func (a *API) deleteGeminiThread(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "delete")
}
func (a *API) geminiEvents(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "events")
}
func (a *API) startGeminiTurn(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "turns")
}
func (a *API) interruptGeminiTurn(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "interrupt")
}
func (a *API) confirmGeminiStopped(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "confirm-stopped")
}
func (a *API) answerGeminiQuestion(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "answer")
}
