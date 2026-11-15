package httpapi

import (
	"errors"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
)

func (a *API) workspaces(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	workspaces, err := a.store.ListWorkspacesFiltered(r.Context(), query.Get("path"), query.Get("server_id"), query.Get("git_status"))
	if errors.Is(err, store.ErrInvalidWorkspaceGitStatus) {
		writeError(w, http.StatusBadRequest, "invalid workspace git status")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workspaces")
		return
	}
	writeJSON(w, http.StatusOK, workspaces)
}
