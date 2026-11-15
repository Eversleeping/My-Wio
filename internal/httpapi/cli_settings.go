package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wio-platform/wio/internal/codexcli"
)

const (
	defaultClaudeTargetVersion     = "2.1.318"
	defaultGeminiTargetVersion     = "0.65.0"
	claudeCLITargetSetting         = "claude_cli_target_version"
	claudeCLIRecentVersionsSetting = "claude_cli_recent_versions"
	geminiCLITargetSetting         = "gemini_cli_target_version"
	geminiCLIRecentVersionsSetting = "gemini_cli_recent_versions"
	codexCLITargetSetting          = "codex_cli_target_version"
	codexCLIRecentVersionsSetting  = "codex_cli_recent_versions"
)

type cliSettingsSpec struct {
	kind, name, target, recent, defaultVersion string
	releases                                   *codexcli.ReleaseChecker
}

func (a *API) cliSettingsSpec(kind string) cliSettingsSpec {
	specs := map[string]cliSettingsSpec{
		"codex":  {kind: "codex", name: "Codex", target: codexCLITargetSetting, recent: codexCLIRecentVersionsSetting, defaultVersion: codexcli.DefaultTargetVersion},
		"gemini": {kind: "gemini", name: "Gemini", target: geminiCLITargetSetting, recent: geminiCLIRecentVersionsSetting, defaultVersion: defaultGeminiTargetVersion},
		"claude": {kind: "claude", name: "Claude", target: claudeCLITargetSetting, recent: claudeCLIRecentVersionsSetting, defaultVersion: defaultClaudeTargetVersion},
	}
	cfg := specs[kind]
	cfg.releases = a.cliChecker(kind)
	return cfg
}

type cliSettingsResponse struct {
	TargetVersion string   `json:"target_version"`
	LatestVersion string   `json:"latest_version,omitempty"`
	Versions      []string `json:"versions,omitempty"`
	Updated       bool     `json:"updated,omitempty"`
}

type cliVersionSelection struct {
	Version string `json:"version"`
}

func (a *API) engineCLISettings(w http.ResponseWriter, r *http.Request, cfg cliSettingsSpec) {
	version, err := a.store.Setting(r.Context(), cfg.target, cfg.defaultVersion)
	if err != nil || !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusInternalServerError, "could not load "+cfg.name+" CLI settings")
		return
	}
	writeJSON(w, http.StatusOK, cliSettingsResponse{TargetVersion: version, Versions: a.cachedCLIVersions(r, cfg)})
}

func (a *API) checkEngineCLIUpdates(w http.ResponseWriter, r *http.Request, cfg cliSettingsSpec) {
	current, err := a.store.Setting(r.Context(), cfg.target, cfg.defaultVersion)
	if err != nil || !codexcli.ValidTargetVersion(current) {
		writeError(w, http.StatusInternalServerError, "could not load "+cfg.name+" CLI settings")
		return
	}
	if cfg.releases == nil {
		writeError(w, http.StatusServiceUnavailable, cfg.name+" CLI release checks are unavailable")
		return
	}
	versions, err := cfg.releases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not check "+cfg.name+" CLI releases", "error", err)
		writeError(w, http.StatusBadGateway, "could not check "+cfg.name+" CLI releases")
		return
	}
	latest := versions[0]
	updated := codexcli.UpdateAvailable(current, latest)
	if err := a.saveCLIVersions(r, versions, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save "+cfg.name+" CLI release list")
		return
	}
	if updated {
		if err := a.store.SetSetting(r.Context(), cfg.target, latest); err != nil {
			writeError(w, http.StatusInternalServerError, "could not save "+cfg.name+" CLI target version")
			return
		}
		current = latest
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, cfg.kind+".cli.release.check", "setting", cfg.target, map[string]any{"target_version": current, "latest_version": latest, "updated": updated}, clientIP(r))
	writeJSON(w, http.StatusOK, cliSettingsResponse{TargetVersion: current, LatestVersion: latest, Versions: versions, Updated: updated})
}

func (a *API) selectEngineCLIVersion(w http.ResponseWriter, r *http.Request, cfg cliSettingsSpec) {
	var input cliVersionSelection
	if !decodeJSON(w, r, &input) {
		return
	}
	version := strings.TrimSpace(input.Version)
	if !codexcli.ValidTargetVersion(version) {
		writeError(w, http.StatusBadRequest, "invalid "+cfg.name+" CLI version")
		return
	}
	if cfg.releases == nil {
		writeError(w, http.StatusServiceUnavailable, cfg.name+" CLI release checks are unavailable")
		return
	}
	versions, err := cfg.releases.RecentStable(r.Context(), 5)
	if err != nil {
		a.log.Warn("could not validate "+cfg.name+" CLI version", "error", err)
		writeError(w, http.StatusBadGateway, "could not check "+cfg.name+" CLI releases")
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
		writeError(w, http.StatusBadRequest, cfg.name+" CLI version is not among the five most recent formal releases")
		return
	}
	if err := a.saveCLIVersions(r, versions, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save "+cfg.name+" CLI release list")
		return
	}
	if err := a.store.SetSetting(r.Context(), cfg.target, version); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save "+cfg.name+" CLI target version")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, cfg.kind+".cli.release.select", "setting", cfg.target, map[string]string{"target_version": version}, clientIP(r))
	writeJSON(w, http.StatusOK, cliSettingsResponse{TargetVersion: version, LatestVersion: versions[0], Versions: versions})
}

func (a *API) cachedCLIVersions(r *http.Request, cfg cliSettingsSpec) []string {
	raw, err := a.store.Setting(r.Context(), cfg.recent, "")
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

func (a *API) saveCLIVersions(r *http.Request, versions []string, cfg cliSettingsSpec) error {
	raw, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	return a.store.SetSetting(r.Context(), cfg.recent, string(raw))
}

type codexCLISettingsResponse = cliSettingsResponse
type codexCLIVersionSelection = cliVersionSelection

func (a *API) codexCLISettings(w http.ResponseWriter, r *http.Request) {
	a.engineCLISettings(w, r, a.cliSettingsSpec("codex"))
}
func (a *API) checkCodexCLIUpdates(w http.ResponseWriter, r *http.Request) {
	a.checkEngineCLIUpdates(w, r, a.cliSettingsSpec("codex"))
}
func (a *API) selectCodexCLIVersion(w http.ResponseWriter, r *http.Request) {
	a.selectEngineCLIVersion(w, r, a.cliSettingsSpec("codex"))
}

type geminiCLISettingsResponse = cliSettingsResponse
type geminiCLIVersionSelection = cliVersionSelection

func (a *API) geminiCLISettings(w http.ResponseWriter, r *http.Request) {
	a.engineCLISettings(w, r, a.cliSettingsSpec("gemini"))
}
func (a *API) checkGeminiCLIUpdates(w http.ResponseWriter, r *http.Request) {
	a.checkEngineCLIUpdates(w, r, a.cliSettingsSpec("gemini"))
}
func (a *API) selectGeminiCLIVersion(w http.ResponseWriter, r *http.Request) {
	a.selectEngineCLIVersion(w, r, a.cliSettingsSpec("gemini"))
}

type claudeCLISettingsResponse = cliSettingsResponse
type claudeCLIVersionSelection = cliVersionSelection

func (a *API) claudeCLISettings(w http.ResponseWriter, r *http.Request) {
	a.engineCLISettings(w, r, a.cliSettingsSpec("claude"))
}
func (a *API) checkClaudeCLIUpdates(w http.ResponseWriter, r *http.Request) {
	a.checkEngineCLIUpdates(w, r, a.cliSettingsSpec("claude"))
}
func (a *API) selectClaudeCLIVersion(w http.ResponseWriter, r *http.Request) {
	a.selectEngineCLIVersion(w, r, a.cliSettingsSpec("claude"))
}
