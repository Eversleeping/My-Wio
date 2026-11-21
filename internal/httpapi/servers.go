package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *API) servers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list servers")
		return
	}
	packagesAvailable := false
	if a.agentUpdates != nil {
		_, packagesErr := a.agentUpdates.Command()
		packagesAvailable = packagesErr == nil
	}
	codexTarget, err := a.store.Setting(r.Context(), codexCLITargetSetting, codexcli.DefaultTargetVersion)
	if err != nil || !codexcli.ValidTargetVersion(codexTarget) {
		writeError(w, http.StatusInternalServerError, "could not load Codex CLI settings")
		return
	}
	for index := range servers {
		servers[index].AgentTargetVersion = buildinfo.Version
		servers[index].AgentUpdateSupported = servers[index].Supports(protocol.CapabilityAgentSelfUpdate)
		available := packagesAvailable
		if servers[index].OS != "" && a.agentUpdates != nil {
			_, err := a.agentUpdates.Package(servers[index].OS + "-" + servers[index].Architecture)
			available = err == nil
		}
		servers[index].AgentRollbackSupported = !servers[index].IsControlPlane && servers[index].AgentUpdateSupported && buildinfo.VersionAtLeast(servers[index].AgentVersion, "0.3.47")
		servers[index].AgentUpdateAvailable = available && servers[index].AgentUpdateSupported && buildinfo.UpdateAvailable(servers[index].AgentVersion, buildinfo.Version)
		servers[index].CodexTargetVersion = codexTarget
		servers[index].CodexUpdateSupported = servers[index].Supports(protocol.CapabilityCodexCLIUpdate)
		servers[index].GeminiCredentialsSupported = servers[index].Supports(protocol.CapabilityGeminiCredentials)
		servers[index].ScopedCredentialsSupported = servers[index].Supports(protocol.CapabilityCredentialsScoped)
		servers[index].ClaudeEffortSupported = servers[index].Supports(protocol.CapabilityClaudeEffort)
		servers[index].ClaudeCredentialsSupported = servers[index].Supports(protocol.CapabilityClaudeManage)
		servers[index].AgentAccessSupported = !servers[index].IsControlPlane && servers[index].Supports(protocol.CapabilityAgentAccess)
		servers[index].AgentRestartSupported = !servers[index].IsControlPlane && servers[index].Supports(protocol.CapabilityAgentRestart)
		servers[index].CLIManagementSupported = servers[index].Supports(protocol.CapabilityCLIManage)
		servers[index].CodexUpdateAvailable = servers[index].CodexUpdateSupported && (servers[index].CodexVersion == "" || codexcli.UpdateAvailable(servers[index].CodexVersion, codexTarget))
		servers[index].ExactRollbackSupported = servers[index].Supports(protocol.CapabilityDeployRollbackExact)
	}
	writeJSON(w, http.StatusOK, servers)
}

func normalizeServerMetadata(address, configuration, notes string) (store.ServerMetadata, error) {
	metadata := store.ServerMetadata{
		Address:       strings.TrimSpace(address),
		Configuration: strings.TrimSpace(configuration),
		Notes:         strings.TrimSpace(notes),
	}
	if utf8.RuneCountInString(metadata.Address) > serverAddressLimit {
		return store.ServerMetadata{}, fmt.Errorf("server address must be %d characters or fewer", serverAddressLimit)
	}
	if utf8.RuneCountInString(metadata.Configuration) > serverConfigurationLimit {
		return store.ServerMetadata{}, fmt.Errorf("server configuration must be %d characters or fewer", serverConfigurationLimit)
	}
	if utf8.RuneCountInString(metadata.Notes) > serverNotesLimit {
		return store.ServerMetadata{}, fmt.Errorf("server notes must be %d characters or fewer", serverNotesLimit)
	}
	return metadata, nil
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Address       string `json:"address"`
		Configuration string `json:"configuration"`
		Notes         string `json:"notes"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	metadata, err := normalizeServerMetadata(input.Address, input.Configuration, input.Notes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := chi.URLParam(r, "serverID")
	updated, err := a.store.UpdateServerMetadata(r.Context(), id, metadata)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update server information")
		return
	}
	if !updated {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.metadata.update", "server", id, map[string]bool{
		"address": metadata.Address != "", "configuration": metadata.Configuration != "", "notes": metadata.Notes != "",
	}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) createEnrollment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name          string   `json:"name"`
		ScanRoots     []string `json:"scan_roots"`
		Address       string   `json:"address"`
		Configuration string   `json:"configuration"`
		Notes         string   `json:"notes"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(input.ScanRoots) == 0 {
		input.ScanRoots = []string{"/srv", "/opt", "/home"}
	}
	for _, root := range input.ScanRoots {
		if !strings.HasPrefix(root, "/") {
			writeError(w, http.StatusBadRequest, "scan roots must be absolute Linux paths")
			return
		}
	}
	metadata, err := normalizeServerMetadata(input.Address, input.Configuration, input.Notes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, err := security.RandomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate enrollment token")
		return
	}
	expires := time.Now().UTC().Add(15 * time.Minute)
	id, err := a.store.CreateEnrollmentWithMetadata(r.Context(), input.Name, input.ScanRoots, token, expires, metadata)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create enrollment")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.enrollment.create", "enrollment", id, map[string]any{"name": input.Name, "scan_roots": input.ScanRoots, "has_address": metadata.Address != "", "has_configuration": metadata.Configuration != "", "has_notes": metadata.Notes != ""}, clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "token": token, "expires_at": expires})
}

func (a *API) revokeServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "serverID")
	err := a.store.RevokeServer(r.Context(), id)
	if errors.Is(err, store.ErrControlPlaneServer) {
		writeError(w, http.StatusForbidden, "the control-plane server cannot be revoked")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeStoreError(w, err, "could not revoke server")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.revoke", "server", id, nil, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	points, err := a.store.Metrics(r.Context(), chi.URLParam(r, "serverID"), time.Now().UTC().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load metrics")
		return
	}
	writeJSON(w, http.StatusOK, points)
}

func (a *API) operationMetrics(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 720 {
		hours = 24
	}
	metrics, err := a.store.OperationMetrics(r.Context(), chi.URLParam(r, "serverID"), time.Now().UTC().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load operation metrics")
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}
