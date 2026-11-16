package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) updateCodexCLI(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.Server(r.Context(), chi.URLParam(r, "serverID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	if !server.Supports(protocol.CapabilityCodexCLIUpdate) {
		writeError(w, http.StatusConflict, "update Agent before updating Codex CLI")
		return
	}
	version, err := a.store.Setting(r.Context(), codexCLITargetSetting, codexcli.DefaultTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusInternalServerError, "could not load Codex CLI settings")
		return
	}
	if server.CodexVersion != "" && !codexcli.UpdateAvailable(server.CodexVersion, version) {
		writeError(w, http.StatusConflict, "Codex CLI is already at or newer than the target version")
		return
	}
	active, err := a.store.HasActiveOperation(r.Context(), server.ID, protocol.OpCodexUpdate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check Codex CLI update queue")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "a Codex CLI update is already queued")
		return
	}
	command := protocol.CodexUpdateCommand{Version: version}
	operationID, err := a.store.QueueOperation(r.Context(), server.ID, protocol.OpCodexUpdate, command, "codex-update:"+server.ID+":"+version+":"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Codex CLI update")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.cli.update", "server", server.ID, map[string]string{"operation_id": operationID, "from_version": server.CodexVersion, "to_version": version}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "version": version})
}
