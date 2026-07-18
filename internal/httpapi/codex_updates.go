package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

const codexCLITargetSetting = "codex_cli_target_version"
const codexCLIRecentVersionsSetting = "codex_cli_recent_versions"

type codexCLISettingsResponse struct {
	TargetVersion string   `json:"target_version"`
	LatestVersion string   `json:"latest_version,omitempty"`
	Versions      []string `json:"versions,omitempty"`
	Updated       bool     `json:"updated,omitempty"`
}

type codexCLIVersionSelection struct {
	Version string `json:"version"`
}

func (a *API) codexCLISettings(w http.ResponseWriter, r *http.Request) {
	version, err := a.store.Setting(r.Context(), codexCLITargetSetting, codexcli.DefaultTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusInternalServerError, "could not load Codex CLI settings")
		return
	}
	writeJSON(w, http.StatusOK, codexCLISettingsResponse{TargetVersion: version, Versions: a.cachedCodexCLIVersions(r)})
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
	versions, err := a.codexReleases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not check Codex CLI releases", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Codex CLI releases")
		return
	}
	latest := versions[0]
	updated := codexcli.UpdateAvailable(current, latest)
	if err := a.saveCodexCLIVersions(r, versions); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Codex CLI release list")
		return
	}
	if updated {
		if err := a.store.SetSetting(r.Context(), codexCLITargetSetting, latest); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save Codex CLI target version")
			return
		}
		current = latest
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.cli.release.check", "setting", codexCLITargetSetting, map[string]any{"target_version": current, "latest_version": latest, "updated": updated}, clientIP(r))
	writeJSON(w, http.StatusOK, codexCLISettingsResponse{TargetVersion: current, LatestVersion: latest, Versions: versions, Updated: updated})
}

func (a *API) selectCodexCLIVersion(w http.ResponseWriter, r *http.Request) {
	var input codexCLIVersionSelection
	if !decodeJSON(w, r, &input) {
		return
	}
	version := strings.TrimSpace(input.Version)
	if !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusBadRequest, "invalid Codex CLI version")
		return
	}
	if a.codexReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "Codex CLI release checks are unavailable")
		return
	}
	versions, err := a.codexReleases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not validate Codex CLI version", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Codex CLI releases")
		return
	}
	allowed := false
	for _, candidate := range versions {
		if candidate == version {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusBadRequest, "Codex CLI version is not among the five most recent formal releases")
		return
	}
	if err := a.saveCodexCLIVersions(r, versions); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Codex CLI release list")
		return
	}
	if err := a.store.SetSetting(r.Context(), codexCLITargetSetting, version); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Codex CLI target version")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.cli.release.select", "setting", codexCLITargetSetting, map[string]string{"target_version": version}, clientIP(r))
	writeJSON(w, http.StatusOK, codexCLISettingsResponse{TargetVersion: version, LatestVersion: versions[0], Versions: versions})
}

func (a *API) cachedCodexCLIVersions(r *http.Request) []string {
	raw, err := a.store.Setting(r.Context(), codexCLIRecentVersionsSetting, "")
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var versions []string
	if err := json.Unmarshal([]byte(raw), &versions); err != nil {
		return nil
	}
	valid := versions[:0]
	for _, version := range versions {
		if codexcli.ValidTargetVersion(version) {
			valid = append(valid, version)
		}
	}
	if len(valid) > 5 {
		valid = valid[:5]
	}
	return valid
}

func (a *API) saveCodexCLIVersions(r *http.Request, versions []string) error {
	raw, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	return a.store.SetSetting(r.Context(), codexCLIRecentVersionsSetting, string(raw))
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
