package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
)

func (a *API) answerClaudeQuestion(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	if !thread.QuestionsSupported {
		writeError(w, 409, "Claude questions require Agent 0.2.98 and Claude Code 2.1.286 or later")
		return
	}
	var input struct {
		Answers map[string]protocol.UserInputAnswer `json:"answers"`
	}
	if !decodeJSONLimit(w, r, &input, 128<<10) {
		return
	}
	id, err := a.store.QueueClaudeAnswer(r.Context(), thread.ID, chi.URLParam(r, "requestID"), input.Answers)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}
