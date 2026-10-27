package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func (a *API) projectServerScope(w http.ResponseWriter, r *http.Request, projectID string) (string, bool) {
	values, supplied := r.URL.Query()["server_id"]
	if !supplied {
		return "", true
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		writeError(w, http.StatusBadRequest, "server_id is required")
		return "", false
	}
	id := strings.TrimSpace(values[0])
	if _, err := a.store.ProjectOnServer(r.Context(), projectID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found on server")
		} else {
			writeError(w, http.StatusInternalServerError, "could not load server project")
		}
		return "", false
	}
	return id, true
}
