package httpapi

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) agentAccess(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	server, err := a.store.Server(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "server not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	if server.IsControlPlane {
		writeError(w, 409, "the built-in Agent's runtime permissions are managed by its deployment")
		return
	}
	if server.Status != "online" {
		writeError(w, 409, "server is offline")
		return
	}
	restarting := strings.HasSuffix(r.URL.Path, "/agent-restart")
	capability := protocol.CapabilityAgentAccess
	if restarting {
		capability = protocol.CapabilityAgentRestart
	}
	if !server.Supports(capability) {
		writeError(w, 409, "upgrade this server's Agent to configure runtime settings")
		return
	}
	command := protocol.AgentAccessCommand{ServerID: id}
	kind := protocol.OpAgentAccessRead
	var operation string
	if restarting {
		kind = protocol.OpAgentRestart
		operation, err = a.store.QueueAgentRestart(r.Context(), id, command)
	} else if r.Method == http.MethodPut {
		var settings protocol.AgentAccessSettings
		if !decodeJSON(w, r, &settings) {
			return
		}
		revision, e := hex.DecodeString(settings.Revision)
		if e != nil || len(revision) != 32 || settings.SessionIsolation {
			writeError(w, 400, "invalid runtime settings or revision")
			return
		}
		command.Settings = &settings
		kind = protocol.OpAgentAccessUpdate
		operation, err = a.store.QueueAgentAccessUpdate(r.Context(), id, command)
	} else {
		operation, err = a.store.QueueResourceOperation(r.Context(), id, kind, command, "agent-access-read:"+store.NewID(), store.OperationResource{}, false)
	}
	if errors.Is(err, store.ErrAgentUpdateBusy) {
		writeError(w, 409, "finish active sessions and server operations before changing runtime settings")
		return
	}
	if err != nil {
		writeError(w, 500, "could not queue runtime settings operation")
		return
	}
	a.gateway.Wake(id)
	if kind == protocol.OpAgentAccessUpdate {
		_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server.access.update", "server", id, map[string]any{"operation_id": operation, "allow_sudo": command.Settings.AllowSudo}, clientIP(r))
	}
	if kind == protocol.OpAgentRestart {
		_ = a.store.Audit(r.Context(), currentSession(r).UserID, "agent.restart", "server", id, map[string]string{"operation_id": operation}, clientIP(r))
	}
	writeJSON(w, 202, map[string]string{"operation_id": operation})
}
func (a *API) agentAccessResult(w http.ResponseWriter, r *http.Request) {
	op, err := a.store.Operation(r.Context(), chi.URLParam(r, "operationID"))
	if errors.Is(err, sql.ErrNoRows) || err == nil && (op.ServerID != chi.URLParam(r, "serverID") || (op.Kind != protocol.OpAgentAccessRead && op.Kind != protocol.OpAgentAccessUpdate && op.Kind != protocol.OpAgentRestart)) {
		writeError(w, 404, "runtime settings operation not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load runtime settings operation")
		return
	}
	response := map[string]any{"status": op.Status}
	if op.Status == "succeeded" {
		var settings protocol.AgentAccessSettings
		if json.Unmarshal([]byte(op.ResultData), &settings) != nil || !validAgentAccessResult(settings) {
			writeError(w, 500, "invalid runtime settings result")
			return
		}
		response["settings"] = settings
	} else if op.Status == "failed" || op.Status == "cancelled" || op.Status == "interrupted" {
		response["error"] = op.Result
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, response)
}

func validAgentAccessResult(settings protocol.AgentAccessSettings) bool {
	revision, err := hex.DecodeString(settings.Revision)
	return err == nil && len(revision) == 32 && !settings.SessionIsolation
}
