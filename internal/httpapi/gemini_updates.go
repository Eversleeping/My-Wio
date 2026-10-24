package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wio-platform/wio/internal/codexcli"
)

const defaultGeminiTargetVersion = "0.65.0"

const geminiCLITargetSetting = "gemini_cli_target_version"
const geminiCLIRecentVersionsSetting = "gemini_cli_recent_versions"

type geminiCLISettingsResponse struct {
	TargetVersion string   `json:"target_version"`
	LatestVersion string   `json:"latest_version,omitempty"`
	Versions      []string `json:"versions,omitempty"`
	Updated       bool     `json:"updated,omitempty"`
}

type geminiCLIVersionSelection struct {
	Version string `json:"version"`
}

func (a *API) geminiCLISettings(w http.ResponseWriter, r *http.Request) {
	version, err := a.store.Setting(r.Context(), geminiCLITargetSetting, defaultGeminiTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusInternalServerError, "could not load Gemini CLI settings")
		return
	}
	writeJSON(w, http.StatusOK, geminiCLISettingsResponse{TargetVersion: version, Versions: a.cachedGeminiCLIVersions(r)})
}

func (a *API) checkGeminiCLIUpdates(w http.ResponseWriter, r *http.Request) {
	current, err := a.store.Setting(r.Context(), geminiCLITargetSetting, defaultGeminiTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(current) {
		writeError(w, http.StatusInternalServerError, "could not load Gemini CLI settings")
		return
	}
	if a.geminiReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "Gemini CLI release checks are unavailable")
		return
	}
	versions, err := a.geminiReleases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not check Gemini CLI releases", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Gemini CLI releases")
		return
	}
	latest := versions[0]
	updated := codexcli.UpdateAvailable(current, latest)
	if err := a.saveGeminiCLIVersions(r, versions); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Gemini CLI release list")
		return
	}
	if updated {
		if err := a.store.SetSetting(r.Context(), geminiCLITargetSetting, latest); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save Gemini CLI target version")
			return
		}
		current = latest
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "gemini.cli.release.check", "setting", geminiCLITargetSetting, map[string]any{"target_version": current, "latest_version": latest, "updated": updated}, clientIP(r))
	writeJSON(w, http.StatusOK, geminiCLISettingsResponse{TargetVersion: current, LatestVersion: latest, Versions: versions, Updated: updated})
}

func (a *API) selectGeminiCLIVersion(w http.ResponseWriter, r *http.Request) {
	var input geminiCLIVersionSelection
	if !decodeJSON(w, r, &input) {
		return
	}
	version := strings.TrimSpace(input.Version)
	if !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusBadRequest, "invalid Gemini CLI version")
		return
	}
	if a.geminiReleases == nil {
		writeError(w, http.StatusServiceUnavailable, "Gemini CLI release checks are unavailable")
		return
	}
	versions, err := a.geminiReleases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not validate Gemini CLI version", "error", err)
		writeError(w, http.StatusBadGateway, "could not check Gemini CLI releases")
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
		writeError(w, http.StatusBadRequest, "Gemini CLI version is not among the five most recent formal releases")
		return
	}
	if err := a.saveGeminiCLIVersions(r, versions); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Gemini CLI release list")
		return
	}
	if err := a.store.SetSetting(r.Context(), geminiCLITargetSetting, version); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save Gemini CLI target version")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "gemini.cli.release.select", "setting", geminiCLITargetSetting, map[string]string{"target_version": version}, clientIP(r))
	writeJSON(w, http.StatusOK, geminiCLISettingsResponse{TargetVersion: version, LatestVersion: versions[0], Versions: versions})
}

func (a *API) cachedGeminiCLIVersions(r *http.Request) []string {
	raw, err := a.store.Setting(r.Context(), geminiCLIRecentVersionsSetting, "")
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

func (a *API) saveGeminiCLIVersions(r *http.Request, versions []string) error {
	raw, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	return a.store.SetSetting(r.Context(), geminiCLIRecentVersionsSetting, string(raw))
}
