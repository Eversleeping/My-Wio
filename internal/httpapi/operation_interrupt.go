package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) interruptOperation(w http.ResponseWriter, r *http.Request) {
	operation, err := a.store.Operation(r.Context(), chi.URLParam(r, "operationID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load operation")
		return
	}
	if !protocol.CanInterruptOperation(operation.Kind) || operation.WorkspaceWrite != 0 {
		writeError(w, http.StatusConflict, "operation does not support interruption")
		return
	}
	server, err := a.store.Server(r.Context(), operation.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if !buildinfo.SupportsOperationInterrupt(server.AgentVersion) {
		writeError(w, http.StatusConflict, "update Agent to 0.2.96 or later to interrupt operations")
		return
	}
	interruptID, err := a.store.RequestOperationInterrupt(r.Context(), operation.ID)
	if errors.Is(err, store.ErrOperationNotActive) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue operation interruption")
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "operation.interrupt", "operation", operation.ID, map[string]string{"interrupt_operation_id": interruptID, "kind": operation.Kind}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": interruptID})
}
