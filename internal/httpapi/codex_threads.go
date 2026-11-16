package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func parseThreadPageValue(values []string, present bool, name string, fallback int, positive bool) (int, error) {
	if !present {
		return fallback, nil
	}
	kind := "non-negative"
	if positive {
		kind = "positive"
	}
	if len(values) != 1 || values[0] == "" {
		return 0, fmt.Errorf("%s must be a %s decimal integer", name, kind)
	}
	for _, character := range values[0] {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%s must be a %s decimal integer", name, kind)
		}
	}
	parsed, err := strconv.ParseInt(values[0], 10, 0)
	if err != nil || (positive && parsed == 0) {
		return 0, fmt.Errorf("%s must be a %s decimal integer", name, kind)
	}
	return int(parsed), nil
}

func (a *API) archiveProjectThreads(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), projectID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	serverID, ok := a.projectServerScope(w, r, projectID)
	if !ok {
		return
	}
	count, err := a.store.ArchiveProjectThreads(r.Context(), projectID, serverID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not archive project sessions")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.archive_project", "project", projectID, map[string]any{"archived": count, "server_id": serverID}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]int64{"archived": count})
}
