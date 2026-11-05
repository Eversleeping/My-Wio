package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wio-platform/wio/internal/codexcli"
)

const defaultClaudeTargetVersion = "2.1.318"

const claudeCLITargetSetting = "claude_cli_target_version"
const claudeCLIRecentVersionsSetting = "claude_cli_recent_versions"

type claudeCLISettingsResponse struct {
	TargetVersion string   `json:"target_version"`
	LatestVersion string   `json:"latest_version,omitempty"`
	Versions      []string `json:"versions,omitempty"`
	Updated       bool     `json:"updated,omitempty"`
}

type claudeCLIVersionSelection struct {
	Version string `json:"version"`
}

func (a *API) claudeCLISettings(w http.ResponseWriter, r *http.Request) {
	version, err := a.store.Setting(r.Context(), claudeCLITargetSetting, defaultClaudeTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusInternalServerError, "could not load Claude CLI settings")
		return
	}
	writeJSON(w, http.StatusOK, claudeCLISettingsResponse{TargetVersion: version, Versions: a.cachedClaudeCLIVersions(r)})
}

func (a *API) checkClaudeCLIUpdates(w http.ResponseWriter, r *http.Request) {
	current, err := a.store.Setting(r.Context(), claudeCLITargetSetting, defaultClaudeTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(current) {
		writeError(w, http.StatusInternalServerError, "could not load Claude CLI settings")
		return
	}
	if a.claudeReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "Claude CLI release checks are unavailable")
		return
	}
	versions, err := a.claudeReleases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not check Claude CLI releases", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Claude CLI releases")
		return
	}
	latest := versions[0]
	updated := codexcli.UpdateAvailable(current, latest)
	if err := a.saveClaudeCLIVersions(r, versions); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Claude CLI release list")
		return
	}
	if updated {
		if err := a.store.SetSetting(r.Context(), claudeCLITargetSetting, latest); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save Claude CLI target version")
			return
		}
		current = latest
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "claude.cli.release.check", "setting", claudeCLITargetSetting, map[string]any{"target_version": current, "latest_version": latest, "updated": updated}, clientIP(r))
	writeJSON(w, http.StatusOK, claudeCLISettingsResponse{TargetVersion: current, LatestVersion: latest, Versions: versions, Updated: updated})
}

func (a *API) selectClaudeCLIVersion(w http.ResponseWriter, r *http.Request) {
	var input claudeCLIVersionSelection
	if !decodeJSON(w, r, &input) {
		return
	}
	version := strings.TrimSpace(input.Version)
	if !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusBadRequest, "invalid Claude CLI version")
		return
	}
	if a.claudeReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "Claude CLI release checks are unavailable")
		return
	}
	versions, err := a.claudeReleases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not validate Claude CLI version", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Claude CLI releases")
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
		writeError(w, http.StatusBadRequest, "Claude CLI version is not among the five most recent formal releases")
		return
	}
	if err := a.saveClaudeCLIVersions(r, versions); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Claude CLI release list")
		return
	}
	if err := a.store.SetSetting(r.Context(), claudeCLITargetSetting, version); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Claude CLI target version")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "claude.cli.release.select", "setting", claudeCLITargetSetting, map[string]string{"target_version": version}, clientIP(r))
	writeJSON(w, http.StatusOK, claudeCLISettingsResponse{TargetVersion: version, LatestVersion: versions[0], Versions: versions})
}

func (a *API) cachedClaudeCLIVersions(r *http.Request) []string {
	raw, err := a.store.Setting(r.Context(), claudeCLIRecentVersionsSetting, "")
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

func (a *API) saveClaudeCLIVersions(r *http.Request, versions []string) error {
	raw, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	return a.store.SetSetting(r.Context(), claudeCLIRecentVersionsSetting, string(raw))
}
