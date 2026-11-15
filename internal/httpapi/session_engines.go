package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/sessionservice"
)

func (a *API) workspaceSessionEngines(w http.ResponseWriter, r *http.Request) {
	service := sessionservice.Service{Store: a.store}
	result, err := service.WorkspaceEngines(r.Context(), chi.URLParam(r, "workspaceID"))
	if err != nil {
		writeStoreError(w, err, "could not load workspace engines")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
