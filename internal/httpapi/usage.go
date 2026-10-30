package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (a *API) geminiUsage(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	usage, err := a.store.SessionUsage(r.Context(), thread.ID, "gemini", thread.Status == "running" || thread.Status == "interrupting")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session usage")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (a *API) threadUsage(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session")
		return
	}
	usage, err := a.store.SessionUsage(r.Context(), thread.ID, "codex", thread.Status == "running")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session usage")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}
