package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) sessionUsage(w http.ResponseWriter, r *http.Request, engine, prefix string) {
	usage, err := a.store.SessionUsageForRef(r.Context(), session.Ref{Engine: engine, ID: chi.URLParam(r, "threadID")})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, prefix+"session not found")
		return
	}
	var lookup store.SessionLookupError
	if errors.As(err, &lookup) {
		writeError(w, 500, "could not load "+prefix+"session")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load session usage")
		return
	}
	writeJSON(w, 200, usage)
}
