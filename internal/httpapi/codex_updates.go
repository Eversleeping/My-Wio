package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

const codexCLITargetSetting = "codex_cli_target_version"

type codexCLISettingsResponse struct {
	TargetVersion string `json:"target_version"`
	LatestVersion string `json:"latest_version,omitempty"`
	Updated       bool   `json:"updated,omitempty"`
}

func (a *API) codexCLISettings(w http.ResponseWriter, r *http.Request) {
	version, err := a.store.Setting(r.Context(), codexCLITargetSetting, codexcli.DefaultTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusInternalServerError, "could not load Codex CLI settings")
		return
	}
	writeJSON(w, http.StatusOK, codexCLISettingsResponse{TargetVersion: version})
}

func (a *API) checkCodexCLIUpdates(w http.ResponseWriter, r *http.Request) {
	current, err := a.store.Setting(r.Context(), codexCLITargetSetting, codexcli.DefaultTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(current) {
		writeError(w, http.StatusInternalServerError, "could not load Codex CLI settings")
		return
	}
	if a.codexReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "Codex CLI release checks are unavailable")
		return
	}
	latest, err := a.codexReleases.LatestStable(r.Context())
	if err != nil {
		a.log.Warn("could not check Codex CLI releases", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Codex CLI releases")
		return
	}
	updated := codexcli.UpdateAvailable(current, latest)
	if updated {
		if err := a.store.SetSetting(r.Context(), codexCLITargetSetting, latest); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save Codex CLI target version")
			return
		}
		current = latest
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.cli.release.check", "setting", codexCLITargetSetting, map[string]any{"target_version": current, "latest_version": latest, "updated": updated}, clientIP(r))
	writeJSON(w, http.StatusOK, codexCLISettingsResponse{TargetVersion: current, LatestVersion: latest, Updated: updated})
}

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
	if !buildinfo.SupportsCodexUpdate(server.AgentVersion) {
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
	active, err := a.store.HasActiveOperation(r.Context(), server.ID, "codex.update")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check Codex CLI update queue")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "a Codex CLI update is already queued")
		return
	}
	command := protocol.CodexUpdateCommand{Version: version}
	operationID, err := a.store.QueueOperation(r.Context(), server.ID, "codex.update", command, "codex-update:"+server.ID+":"+version+":"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Codex CLI update")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.cli.update", "server", server.ID, map[string]string{"operation_id": operationID, "from_version": server.CodexVersion, "to_version": version}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "version": version})
}
