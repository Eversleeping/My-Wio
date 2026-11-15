package httpapi

import (
	"github.com/wio-platform/wio/internal/session"
	"net/http"
)

func (a *API) answerClaudeQuestion(w http.ResponseWriter, r *http.Request) {
	a.legacySession(w, r, session.Claude, "answer")
}
