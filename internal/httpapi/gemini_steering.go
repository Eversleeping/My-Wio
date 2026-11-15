package httpapi

import (
	"github.com/wio-platform/wio/internal/session"
	"net/http"
)

func (a *API) steerGeminiTurn(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Gemini, "steer")
}
