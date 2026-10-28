package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

type serverCLI struct {
	Kind      string           `json:"kind"`
	Version   string           `json:"version"`
	Ready     bool             `json:"ready"`
	Supported bool             `json:"supported"`
	Operation *store.Operation `json:"operation,omitempty"`
	store.CLIInstallBlockers
}

func (a *API) cliChecker(kind string) *codexcli.ReleaseChecker {
	if kind == "codex" {
		return a.codexReleases
	}
	if kind == "gemini" {
		return a.geminiReleases
	}
	return nil
}

func (a *API) serverCLIs(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.Server(r.Context(), chi.URLParam(r, "serverID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "server not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	supported := buildinfo.SupportsCLIManagement(server.AgentVersion)
	items := []serverCLI{
		{Kind: "codex", Version: server.CodexVersion, Ready: server.CodexReady != 0, Supported: supported},
		{Kind: "gemini", Version: server.GeminiVersion, Ready: server.GeminiReady != 0, Supported: supported},
	}
	for i := range items {
		items[i].CLIInstallBlockers, err = a.store.CLIInstallBlockers(r.Context(), server.ID, items[i].Kind)
		if err != nil {
			writeError(w, 500, "could not load CLI activity")
			return
		}
		items[i].Operation, err = a.store.LatestCLIInstall(r.Context(), server.ID, items[i].Kind)
		if err != nil {
			writeError(w, 500, "could not load CLI installation status")
			return
		}
	}
	writeJSON(w, 200, items)
}

func (a *API) cliVersions(w http.ResponseWriter, r *http.Request) {
	checker := a.cliChecker(chi.URLParam(r, "kind"))
	if checker == nil {
		writeError(w, 404, "AI CLI release list unavailable")
		return
	}
	versions, err := checker.RecentStable(r.Context(), 5)
	if err != nil {
		writeError(w, 502, "could not load official CLI releases; retry later")
		return
	}
	target := ""
	if chi.URLParam(r, "kind") == "gemini" {
		target, err = a.store.Setting(r.Context(), geminiCLITargetSetting, defaultGeminiTargetVersion)
	} else {
		target, err = a.store.Setting(r.Context(), codexCLITargetSetting, codexcli.DefaultTargetVersion)
	}
	if err != nil {
		writeError(w, 500, "could not load CLI target")
		return
	}
	writeJSON(w, 200, map[string]any{"versions": versions, "target_version": target})
}

func (a *API) installServerCLI(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	checker := a.cliChecker(kind)
	if checker == nil {
		writeError(w, 404, "unsupported AI CLI")
		return
	}
	server, err := a.store.Server(r.Context(), chi.URLParam(r, "serverID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "server not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, 409, "server is offline")
		return
	}
	if !buildinfo.SupportsCLIManagement(server.AgentVersion) {
		writeError(w, 409, "update Wio Agent to "+buildinfo.MinimumCLIManagementVersion+" or newer first")
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	version := strings.TrimSpace(input.Version)
	if !codexcli.ValidTargetVersion(version) {
		writeError(w, 400, "invalid CLI version")
		return
	}
	versions, err := checker.RecentStable(r.Context(), 5)
	if err != nil {
		writeError(w, 502, "could not validate official CLI release")
		return
	}
	allowed := false
	for _, candidate := range versions {
		if version == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, 400, "select one of the five most recent official stable releases")
		return
	}
	current := server.CodexVersion
	if kind == "gemini" {
		current = server.GeminiVersion
	}
	if codexcli.ReportedVersionMatches(current, version) {
		writeError(w, 409, "selected CLI version is already installed")
		return
	}
	id, err := a.store.QueueCLIInstall(r.Context(), server.ID, protocol.CLIInstallCommand{Kind: kind, Version: version})
	if errors.Is(err, store.ErrCLIInstallBusy) {
		writeJSON(w, 409, map[string]string{"error": err.Error(), "code": "cli_install_busy"})
		return
	}
	if err != nil {
		writeError(w, 500, "could not queue CLI installation")
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "ai.cli.install", "server", server.ID, map[string]string{"kind": kind, "version": version, "operation_id": id}, clientIP(r))
	writeJSON(w, 202, map[string]string{"operation_id": id, "version": version})
}
