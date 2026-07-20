package httpapi

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) summary(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{}
	queries := map[string]string{
		"servers":     "SELECT COUNT(*) FROM servers WHERE revoked_at IS NULL",
		"online":      "SELECT COUNT(*) FROM servers WHERE revoked_at IS NULL AND status='online' AND last_seen_at>?",
		"projects":    "SELECT COUNT(*) FROM projects",
		"threads":     "SELECT COUNT(*) FROM codex_threads",
		"deployments": "SELECT COUNT(*) FROM deployments WHERE status IN ('queued','preparing','running')",
		"alerts":      "SELECT COUNT(*) FROM alerts WHERE status='open'",
	}
	for key, query := range queries {
		var count int
		var err error
		if key == "online" {
			err = a.store.DB.GetContext(r.Context(), &count, a.store.Q(query), time.Now().UTC().Add(-store.ServerOnlineGracePeriod))
		} else {
			err = a.store.DB.GetContext(r.Context(), &count, query)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load dashboard")
			return
		}
		counts[key] = count
	}
	deployments, _ := a.store.ListDeployments(r.Context())
	alerts, _ := a.store.ListAlerts(r.Context())
	if len(deployments) > 5 {
		deployments = deployments[:5]
	}
	if len(alerts) > 5 {
		alerts = alerts[:5]
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "deployments": deployments, "alerts": alerts})
}

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
		servers[index].AgentUpdateSupported = buildinfo.SupportsSelfUpdate(servers[index].AgentVersion)
		servers[index].AgentUpdateAvailable = packagesAvailable && buildinfo.UpdateAvailable(servers[index].AgentVersion, buildinfo.Version)
		servers[index].CodexTargetVersion = codexTarget
		servers[index].CodexUpdateSupported = buildinfo.SupportsCodexUpdate(servers[index].AgentVersion)
		servers[index].CodexUpdateAvailable = servers[index].CodexUpdateSupported && (servers[index].CodexVersion == "" || codexcli.UpdateAvailable(servers[index].CodexVersion, codexTarget))
	}
	writeJSON(w, http.StatusOK, servers)
}

const (
	serverAddressLimit       = 255
	serverConfigurationLimit = 4096
	serverNotesLimit         = 4096
	projectNameLimit         = 200
	projectDestinationLimit  = 1024
	gitBranchNameLimit       = 240
	threadTitleLimit         = 200
)

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
	now := time.Now().UTC()
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Q("UPDATE servers SET revoked_at=?,status='offline' WHERE id=? AND revoked_at IS NULL"), now, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not revoke server")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	_, _ = a.store.DB.ExecContext(r.Context(), a.store.Q("UPDATE agent_credentials SET revoked_at=? WHERE server_id=?"), now, id)
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

func (a *API) projects(w http.ResponseWriter, r *http.Request) {
	projects, err := a.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list projects")
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

type createProjectInput struct {
	Mode             string `json:"mode"`
	Name             string `json:"name"`
	ServerID         string `json:"server_id"`
	Destination      string `json:"destination"`
	InitialBranch    string `json:"initial_branch"`
	InitializeREADME bool   `json:"initialize_readme"`
	Remote           struct {
		Mode string `json:"mode"`
	} `json:"remote"`
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var input createProjectInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Mode = strings.TrimSpace(input.Mode)
	input.Name = strings.TrimSpace(input.Name)
	input.ServerID = strings.TrimSpace(input.ServerID)
	input.Destination = strings.TrimSpace(input.Destination)
	input.InitialBranch = strings.TrimSpace(input.InitialBranch)
	input.Remote.Mode = strings.TrimSpace(input.Remote.Mode)
	if input.Mode != "blank" {
		writeError(w, http.StatusBadRequest, "mode must be blank")
		return
	}
	if input.Name == "" || input.ServerID == "" {
		writeError(w, http.StatusBadRequest, "name and server_id are required")
		return
	}
	if utf8.RuneCountInString(input.Name) > projectNameLimit {
		writeError(w, http.StatusBadRequest, "project name is too long")
		return
	}
	if input.InitialBranch == "" {
		input.InitialBranch = "main"
	}
	if len(input.InitialBranch) > gitBranchNameLimit || strings.ContainsAny(input.InitialBranch, "\x00\r\n\t ") || strings.HasPrefix(input.InitialBranch, "-") {
		writeError(w, http.StatusBadRequest, "initial_branch is invalid")
		return
	}
	if len(input.Destination) > projectDestinationLimit || strings.ContainsAny(input.Destination, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "destination is invalid")
		return
	}
	if input.Remote.Mode != "" && input.Remote.Mode != "none" {
		writeError(w, http.StatusBadRequest, "remote repository setup is not available for blank projects yet")
		return
	}
	server, err := a.store.Server(r.Context(), input.ServerID)
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
	if input.InitializeREADME && server.GitProfileID == "" {
		writeError(w, http.StatusConflict, "a Git credential profile with commit identity is required for the initial commit")
		return
	}
	provision, err := a.store.CreateBlankProject(r.Context(), server.ID, input.Name, input.Destination, input.InitialBranch, input.InitializeREADME)
	if err != nil {
		if databaseConflict(err) {
			writeError(w, http.StatusConflict, "project could not be reserved")
		} else {
			writeError(w, http.StatusInternalServerError, "could not create blank project")
		}
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.create", "project", provision.Project.ID, map[string]any{
		"server_id": server.ID, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID,
		"initial_branch": input.InitialBranch, "initialize_readme": input.InitializeREADME,
	}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"project": provision.Project, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID})
}

func (a *API) retryProjectCreation(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	project, err := a.store.Project(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	if project.Status != "failed" && project.Status != "partial" {
		writeError(w, http.StatusConflict, "only a failed or partial blank project can be retried")
		return
	}
	operations, err := a.store.ListProjectOperations(r.Context(), projectID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project operations")
		return
	}
	serverID := ""
	for _, operation := range operations {
		if operation.Kind == "git.project.create" {
			serverID = operation.ServerID
			break
		}
	}
	if serverID == "" {
		writeError(w, http.StatusConflict, "blank project creation operation was not found")
		return
	}
	server, err := a.store.Server(r.Context(), serverID)
	if err != nil || server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	provision, err := a.store.RetryBlankProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.create.retry", "project", projectID, map[string]string{"server_id": server.ID, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"project": provision.Project, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID})
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   *string `json:"name"`
		Pinned *bool   `json:"pinned"`
		Hidden *bool   `json:"hidden"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Name == nil && input.Pinned == nil && input.Hidden == nil {
		writeError(w, http.StatusBadRequest, "at least one project field is required")
		return
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "project name is required")
			return
		}
		if utf8.RuneCountInString(name) > projectNameLimit {
			writeError(w, http.StatusBadRequest, "project name is too long")
			return
		}
		input.Name = &name
	}
	projectID := chi.URLParam(r, "projectID")
	project, err := a.store.UpdateProject(r.Context(), projectID, input.Name, input.Pinned, input.Hidden)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update project")
		return
	}
	detail := map[string]any{}
	if input.Name != nil {
		detail["name"] = *input.Name
	}
	if input.Pinned != nil {
		detail["pinned"] = *input.Pinned
	}
	if input.Hidden != nil {
		detail["hidden"] = *input.Hidden
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.update", "project", project.ID, detail, clientIP(r))
	writeJSON(w, http.StatusOK, project)
}

func (a *API) workspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := a.store.ListWorkspaces(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list workspaces")
		return
	}
	writeJSON(w, http.StatusOK, workspaces)
}

type createWorktreeInput struct {
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	BaseRef string `json:"base_ref"`
}

func (a *API) createWorktree(w http.ResponseWriter, r *http.Request) {
	workspace, err := a.store.Workspace(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil || server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	var input createWorktreeInput
	if !decodeJSON(w, r, &input) {
		return
	}
	command, err := newWorktreeCommand(workspace, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	operationID, err := a.store.QueueOperation(r.Context(), workspace.ServerID, "git.worktree.create", command, "git-worktree:"+command.TargetWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue worktree creation")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "git.worktree.create", "workspace", workspace.ID, map[string]string{"operation_id": operationID, "target_workspace_id": command.TargetWorkspaceID, "branch": command.Branch}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "workspace_id": command.TargetWorkspaceID})
}

func (a *API) forkThreadToWorktree(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	if thread.ArchivedAt != nil || thread.CodexThreadID == "" || thread.Status == "queued" || thread.Status == "running" {
		writeError(w, http.StatusConflict, "Codex session cannot be continued to a worktree")
		return
	}
	workspace, err := a.store.Workspace(r.Context(), thread.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil || server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	var input createWorktreeInput
	if !decodeJSON(w, r, &input) {
		return
	}
	command, err := newWorktreeCommand(workspace, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	command.SourceThreadID = thread.ID
	command.TargetThreadID = store.NewID()
	command.CodexThread = thread.CodexThreadID
	command.Title = thread.Title + " (continued)"
	operationID, err := a.store.QueueOperation(r.Context(), workspace.ServerID, "git.worktree.create", command, "git-worktree-fork:"+command.TargetWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue worktree continuation")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.fork_worktree", "thread", thread.ID, map[string]string{"operation_id": operationID, "target_workspace_id": command.TargetWorkspaceID, "target_thread_id": command.TargetThreadID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "workspace_id": command.TargetWorkspaceID, "target_thread_id": command.TargetThreadID})
}

func newWorktreeCommand(workspace store.Workspace, input createWorktreeInput) (protocol.GitWorktreeCreateCommand, error) {
	branch := strings.TrimSpace(input.Branch)
	if branch == "" || len(branch) > 240 || strings.ContainsAny(branch, "\x00\r\n") {
		return protocol.GitWorktreeCreateCommand{}, errors.New("valid branch is required")
	}
	target := strings.TrimSpace(input.Path)
	if target == "" {
		name := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(branch)
		target = filepath.Join(filepath.Dir(workspace.Path), filepath.Base(workspace.Path)+"-"+name)
	}
	if !filepath.IsAbs(target) {
		return protocol.GitWorktreeCreateCommand{}, errors.New("worktree path must be absolute")
	}
	return protocol.GitWorktreeCreateCommand{SourceWorkspaceID: workspace.ID, TargetWorkspaceID: store.NewID(), ProjectID: workspace.ProjectID, SourcePath: workspace.Path, TargetPath: filepath.Clean(target), Branch: branch, BaseRef: strings.TrimSpace(input.BaseRef)}, nil
}

func (a *API) importProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerID    string `json:"server_id"`
		Name        string `json:"name"`
		RemoteURL   string `json:"remote_url"`
		Destination string `json:"destination"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.RemoteURL = strings.TrimSpace(input.RemoteURL)
	if input.ServerID == "" || input.RemoteURL == "" {
		writeError(w, http.StatusBadRequest, "server_id and remote_url are required")
		return
	}
	if !validGitRemote(input.RemoteURL) {
		writeError(w, http.StatusBadRequest, "remote_url must use HTTPS or SSH")
		return
	}
	if input.Name == "" {
		input.Name = repositoryName(input.RemoteURL)
	}
	project, err := a.store.CreateProject(r.Context(), input.Name, input.RemoteURL)
	if err != nil {
		if databaseConflict(err) {
			writeError(w, http.StatusConflict, "project already exists")
		} else {
			writeError(w, http.StatusInternalServerError, "could not create project")
		}
		return
	}
	command := protocol.GitImportCommand{ProjectID: project.ID, Name: project.Name, RemoteURL: project.RemoteURL, Destination: input.Destination}
	operationID, err := a.store.QueueOperation(r.Context(), input.ServerID, "git.import", command, "git-import:"+project.ID)
	if err != nil {
		_, _ = a.store.DB.ExecContext(r.Context(), a.store.Q("DELETE FROM projects WHERE id=?"), project.ID)
		writeError(w, http.StatusInternalServerError, "could not queue Git import")
		return
	}
	a.gateway.Wake(input.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.import", "project", project.ID, map[string]string{"server_id": input.ServerID, "operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"project": project, "operation_id": operationID})
}

func (a *API) retryProjectImport(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	project, err := a.store.Project(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	latest, err := a.store.LatestProjectImport(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && latest.Status != "failed") {
		writeError(w, http.StatusConflict, "only a failed Git import can be retried")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Git import")
		return
	}
	active, err := a.store.HasActiveProjectImport(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check Git import queue")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "a Git import is already active for this project")
		return
	}
	server, err := a.store.Server(r.Context(), latest.ServerID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "the original target server is unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load target server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "the target server is offline")
		return
	}
	command := protocol.GitImportCommand{ProjectID: project.ID, Name: project.Name, RemoteURL: project.RemoteURL, Destination: latest.Command.Destination}
	operationID, err := a.store.QueueOperation(r.Context(), server.ID, "git.import", command, "git-import-retry:"+project.ID+":"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Git import retry")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.import.retry", "project", project.ID, map[string]string{"server_id": server.ID, "operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	project, err := a.store.Project(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	active, err := a.store.HasActiveProjectImport(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check Git import queue")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "project cannot be deleted while a Git import is active")
		return
	}
	if project.WorkspaceCount > 0 {
		writeError(w, http.StatusConflict, "project cannot be deleted while workspaces exist")
		return
	}
	deleted, err := a.store.DeleteProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete project")
		return
	}
	if !deleted {
		writeError(w, http.StatusConflict, "project cannot be deleted while dependent resources exist")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.delete", "project", project.ID, map[string]string{"name": project.Name}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) discoverProjects(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerID string `json:"server_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ServerID = strings.TrimSpace(input.ServerID)
	if input.ServerID == "" {
		writeError(w, http.StatusBadRequest, "server_id is required")
		return
	}
	server, err := a.store.Server(r.Context(), input.ServerID)
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
	operationID, err := a.store.QueueOperation(r.Context(), server.ID, "inventory.scan", map[string]any{}, "inventory-scan:"+server.ID+":"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue project discovery")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.inventory.scan", "server", server.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) workspaceFiles(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceID")
	if _, err := a.store.Workspace(r.Context(), workspaceID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	snapshot, err := a.store.WorkspaceFileSnapshot(r.Context(), workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"workspace_id": workspaceID, "files": []protocol.WorkspaceFile{}, "truncated": false, "status": "idle", "error": "", "requested_at": nil, "updated_at": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace files")
		return
	}
	var files []protocol.WorkspaceFile
	if err := json.Unmarshal([]byte(snapshot.Files), &files); err != nil {
		writeError(w, http.StatusInternalServerError, "workspace file snapshot is invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": workspaceID, "files": files, "truncated": snapshot.Truncated != 0, "status": snapshot.Status, "error": snapshot.Error, "requested_at": snapshot.RequestedAt, "updated_at": snapshot.UpdatedAt})
}

func (a *API) refreshWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceID")
	workspace, err := a.store.Workspace(r.Context(), workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	command := protocol.WorkspaceFilesCommand{WorkspaceID: workspace.ID, Path: workspace.Path}
	operationID, err := a.store.QueueOperation(r.Context(), workspace.ServerID, "workspace.files", command, "workspace-files:"+workspace.ID+":"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue workspace file scan")
		return
	}
	if err := a.store.BeginWorkspaceFileScan(r.Context(), workspace.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start workspace file scan")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "workspace.files.scan", "workspace", workspace.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) workspaceFilePreview(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceID")
	if _, err := a.store.Workspace(r.Context(), workspaceID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	path, ok := normalizeWorkspaceFilePath(r.URL.Query().Get("path"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid workspace file path")
		return
	}
	preview, err := a.store.WorkspaceFilePreview(r.Context(), workspaceID, path)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"workspace_id": workspaceID, "path": path, "content": "", "size": 0, "truncated": false, "status": "idle", "error": "", "requested_at": nil, "updated_at": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace file preview")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": preview.WorkspaceID, "path": preview.Path, "content": preview.Content, "size": preview.Size, "truncated": preview.Truncated != 0, "status": preview.Status, "error": preview.Error, "requested_at": preview.RequestedAt, "updated_at": preview.UpdatedAt})
}

func (a *API) requestWorkspaceFilePreview(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceID")
	workspace, err := a.store.Workspace(r.Context(), workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	var input struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	path, ok := normalizeWorkspaceFilePath(input.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid workspace file path")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	if err := a.store.BeginWorkspaceFilePreview(r.Context(), workspace.ID, path); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start workspace file preview")
		return
	}
	command := protocol.WorkspaceFilePreviewCommand{WorkspaceID: workspace.ID, Root: workspace.Path, Path: path}
	operationID, err := a.store.QueueOperation(r.Context(), workspace.ServerID, "workspace.file.preview", command, "workspace-file-preview:"+workspace.ID+":"+store.NewID())
	if err != nil {
		_ = a.store.FailWorkspaceFilePreview(r.Context(), workspace.ID, path, "could not queue workspace file preview")
		writeError(w, http.StatusInternalServerError, "could not queue workspace file preview")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "workspace.file.preview", "workspace", workspace.ID, map[string]string{"operation_id": operationID, "path": path}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "path": path})
}

func normalizeWorkspaceFilePath(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || len(value) > 1024 || strings.HasPrefix(value, "/") {
		return "", false
	}
	clean := pathpkg.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func (a *API) threads(w http.ResponseWriter, r *http.Request) {
	archived := r.URL.Query().Get("archived")
	if archived != "" && archived != "true" && archived != "all" {
		writeError(w, http.StatusBadRequest, "archived must be true or all")
		return
	}
	threads, err := a.store.ListThreads(r.Context(), archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list Codex sessions")
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

func (a *API) updateThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title    *string `json:"title"`
		Pinned   *bool   `json:"pinned"`
		Archived *bool   `json:"archived"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Title == nil && input.Pinned == nil && input.Archived == nil {
		writeError(w, http.StatusBadRequest, "at least one thread field is required")
		return
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "thread title is required")
			return
		}
		if utf8.RuneCountInString(title) > threadTitleLimit {
			writeError(w, http.StatusBadRequest, "thread title is too long")
			return
		}
		input.Title = &title
	}
	threadID := chi.URLParam(r, "threadID")
	if input.Archived != nil && *input.Archived {
		current, err := a.store.Thread(r.Context(), threadID)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Codex session not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load Codex session")
			return
		}
		if current.Status == "queued" || current.Status == "running" {
			writeError(w, http.StatusConflict, "active Codex session cannot be archived")
			return
		}
	}
	thread, err := a.store.UpdateThread(r.Context(), threadID, input.Title, input.Pinned, input.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update Codex session")
		return
	}
	detail := map[string]any{}
	if input.Title != nil {
		detail["title"] = *input.Title
	}
	if input.Pinned != nil {
		detail["pinned"] = *input.Pinned
	}
	if input.Archived != nil {
		detail["archived"] = *input.Archived
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.update", "thread", thread.ID, detail, clientIP(r))
	writeJSON(w, http.StatusOK, thread)
}

func (a *API) archiveProjectThreads(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), projectID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	count, err := a.store.ArchiveProjectThreads(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not archive project sessions")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.archive_project", "project", projectID, map[string]int64{"archived": count}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]int64{"archived": count})
}

func (a *API) forkThread(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	if thread.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "archived Codex session is read-only")
		return
	}
	if thread.CodexThreadID == "" {
		writeError(w, http.StatusConflict, "Codex session must be initialized before it can be continued")
		return
	}
	if thread.Status == "queued" || thread.Status == "running" {
		writeError(w, http.StatusConflict, "active Codex session cannot be continued yet")
		return
	}
	server, err := a.store.Server(r.Context(), thread.ServerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	targetID := store.NewID()
	command := protocol.ForkThreadCommand{SourceThreadID: thread.ID, TargetThreadID: targetID, CodexThread: thread.CodexThreadID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Title: thread.Title + " (continued)"}
	opID, err := a.store.QueueOperation(r.Context(), thread.ServerID, "codex.thread.fork", command, "codex-fork:"+targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Codex session continuation")
		return
	}
	a.gateway.Wake(thread.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.fork", "thread", thread.ID, map[string]string{"operation_id": opID, "target_thread_id": targetID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "target_thread_id": targetID})
}

func (a *API) createThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		// Kept for compatibility with older cached web clients. Codex now names threads.
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	thread, err := a.store.CreateThread(r.Context(), input.WorkspaceID, "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not create Codex session")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.create", "thread", thread.ID, map[string]string{"workspace_id": input.WorkspaceID}, clientIP(r))
	writeJSON(w, http.StatusCreated, thread)
}

func (a *API) deleteThread(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "threadID")
	thread, err := a.store.Thread(r.Context(), threadID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	if err := a.store.DeleteThread(r.Context(), threadID); errors.Is(err, store.ErrThreadActive) {
		writeError(w, http.StatusConflict, "active Codex session must be interrupted before deletion")
		return
	} else if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete Codex session")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.delete", "thread", thread.ID, map[string]string{"title": thread.Title, "workspace_id": thread.WorkspaceID}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) threadEvents(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	threadID := chi.URLParam(r, "threadID")
	var events []protocol.StreamEvent
	var err error
	if r.URL.Query().Get("view") == "raw" {
		if after > 0 {
			events, err = a.store.Events(r.Context(), threadID, after, 1000)
		} else {
			events, err = a.store.RecentEvents(r.Context(), threadID, 1000)
		}
	} else {
		events, err = a.store.ConversationEvents(r.Context(), threadID, after, 10000)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session events")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *API) startTurn(w http.ResponseWriter, r *http.Request) {
	a.handleTurn(w, r, "")
}

func (a *API) rewriteTurn(w http.ResponseWriter, r *http.Request) {
	a.handleTurn(w, r, chi.URLParam(r, "eventID"))
}

func (a *API) handleTurn(w http.ResponseWriter, r *http.Request, routeEditEventID string) {
	threadID := chi.URLParam(r, "threadID")
	var input struct {
		Prompt          string               `json:"prompt"`
		Images          []protocol.TurnImage `json:"images"`
		Model           string               `json:"model"`
		ReasoningEffort string               `json:"reasoning_effort"`
		ApprovalMode    string               `json:"approval_mode"`
		EditEventID     string               `json:"edit_event_id"`
	}
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	if routeEditEventID != "" {
		if input.EditEventID != "" && input.EditEventID != routeEditEventID {
			writeError(w, http.StatusBadRequest, "conflicting edit target")
			return
		}
		input.EditEventID = routeEditEventID
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Model = strings.TrimSpace(input.Model)
	input.ReasoningEffort = strings.TrimSpace(input.ReasoningEffort)
	if input.Prompt == "" && len(input.Images) == 0 {
		writeError(w, http.StatusBadRequest, "prompt or image is required")
		return
	}
	if !validTurnImages(input.Images) {
		writeError(w, http.StatusBadRequest, "invalid or oversized turn images")
		return
	}
	if utf8.RuneCountInString(input.Model) > 128 {
		writeError(w, http.StatusBadRequest, "model is too long")
		return
	}
	if !validReasoningEffort(input.ReasoningEffort) {
		writeError(w, http.StatusBadRequest, "invalid reasoning_effort")
		return
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = "on-request"
	}
	if input.ApprovalMode != "on-request" && input.ApprovalMode != "untrusted" && input.ApprovalMode != "never" {
		writeError(w, http.StatusBadRequest, "invalid approval_mode")
		return
	}
	thread, err := a.store.Thread(r.Context(), threadID)
	if err != nil {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if thread.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "archived Codex session is read-only")
		return
	}
	command := protocol.StartTurnCommand{ThreadID: thread.ID, CodexThread: thread.CodexThreadID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: input.Prompt, Images: input.Images, Model: input.Model, ReasoningEffort: input.ReasoningEffort, ApprovalMode: input.ApprovalMode}
	if input.EditEventID != "" {
		operationID, _, err := a.store.RewriteThread(r.Context(), thread, input.EditEventID, command, eventPayload(map[string]any{"text": input.Prompt, "image_count": len(input.Images)}))
		if errors.Is(err, store.ErrThreadActive) {
			writeError(w, http.StatusConflict, "active Codex session must finish before editing an earlier message")
			return
		}
		if errors.Is(err, store.ErrInvalidEditTarget) {
			writeError(w, http.StatusBadRequest, "message is no longer available to edit")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not rewrite Codex session")
			return
		}
		a.gateway.Wake(thread.ServerID)
		session := currentSession(r)
		_ = a.store.Audit(r.Context(), session.UserID, "codex.turn.rewrite", "thread", thread.ID, map[string]string{"operation_id": operationID, "edit_event_id": input.EditEventID}, clientIP(r))
		writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
		return
	}
	if err := a.store.ClaimThreadForTurn(r.Context(), thread.ID); errors.Is(err, store.ErrThreadActive) {
		writeError(w, http.StatusConflict, "active Codex session must finish before starting another turn")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not reserve Codex session")
		return
	}
	operationID, err := a.store.QueueOperation(r.Context(), thread.ServerID, "codex.turn.start", command, "codex-turn:"+store.NewID())
	if err != nil {
		_ = a.store.SetThreadStatus(r.Context(), thread.ID, thread.Status)
		writeError(w, http.StatusInternalServerError, "could not queue Codex turn")
		return
	}
	event, _ := a.store.AddEvent(r.Context(), protocol.StreamEvent{StreamID: thread.ID, Kind: "user.message", Payload: eventPayload(map[string]any{"text": input.Prompt, "image_count": len(input.Images)})})
	a.hub.Publish(event)
	a.gateway.Wake(thread.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.turn.start", "thread", thread.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func validReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func validTurnImages(images []protocol.TurnImage) bool {
	if len(images) > 4 {
		return false
	}
	total := 0
	for _, image := range images {
		prefix, encoded, ok := strings.Cut(image.DataURL, ",")
		if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64" && prefix != "data:image/webp;base64") {
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded) > 2<<20 {
			return false
		}
		if http.DetectContentType(decoded) != strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64") {
			return false
		}
		total += len(decoded)
	}
	return total <= 4<<20
}

func (a *API) interruptTurn(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if thread.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "archived Codex session is read-only")
		return
	}
	turnID, err := a.store.LatestActiveTurnID(r.Context(), thread.ID)
	if errors.Is(err, sql.ErrNoRows) || turnID == "" {
		writeError(w, http.StatusConflict, "active Codex turn is not ready to interrupt")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")
		return
	}
	command := protocol.InterruptTurnCommand{ThreadID: thread.ID, CodexThread: thread.CodexThreadID, TurnID: turnID}
	operationID, err := a.store.QueueOperation(r.Context(), thread.ServerID, "codex.turn.interrupt", command, "codex-interrupt:"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue interrupt")
		return
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

type approvalView struct {
	ID         string          `db:"id" json:"id"`
	ThreadID   string          `db:"thread_id" json:"thread_id"`
	RequestID  string          `db:"request_id" json:"request_id"`
	Kind       string          `db:"kind" json:"kind"`
	Detail     json.RawMessage `db:"-" json:"detail"`
	DetailText string          `db:"detail" json:"-"`
	Status     string          `db:"status" json:"status"`
	Title      string          `db:"title" json:"title"`
	ExpiresAt  time.Time       `db:"expires_at" json:"expires_at"`
}

func (a *API) approvals(w http.ResponseWriter, r *http.Request) {
	var rows []approvalView
	err := a.store.DB.SelectContext(r.Context(), &rows, `SELECT a.id,a.thread_id,a.request_id,a.kind,a.detail,a.status,t.title,a.expires_at FROM approvals a JOIN codex_threads t ON t.id=a.thread_id WHERE a.status='pending' ORDER BY a.expires_at`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list approvals")
		return
	}
	for index := range rows {
		rows[index].Detail = json.RawMessage(rows[index].DetailText)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (a *API) decideApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Decision string `json:"decision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Decision != "approved" && input.Decision != "denied" {
		writeError(w, http.StatusBadRequest, "decision must be approved or denied")
		return
	}
	var approval struct {
		ID        string `db:"id"`
		ThreadID  string `db:"thread_id"`
		RequestID string `db:"request_id"`
		ServerID  string `db:"server_id"`
	}
	err := a.store.DB.GetContext(r.Context(), &approval, a.store.Q(`SELECT a.id,a.thread_id,a.request_id,w.server_id FROM approvals a JOIN codex_threads t ON t.id=a.thread_id JOIN workspaces w ON w.id=t.workspace_id WHERE a.id=? AND a.status='pending' AND a.expires_at>?`), chi.URLParam(r, "approvalID"), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusNotFound, "pending approval not found")
		return
	}
	command := protocol.ApprovalDecisionCommand{ThreadID: approval.ThreadID, RequestID: approval.RequestID, Decision: input.Decision}
	operationID, err := a.store.ResolveApprovalAndQueue(r.Context(), approval.ID, approval.ServerID, input.Decision, command)
	if errors.Is(err, store.ErrApprovalResolved) {
		writeError(w, http.StatusConflict, "approval was already resolved")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not send approval decision")
		return
	}
	a.gateway.Wake(approval.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.approval.decide", "approval", approval.ID, map[string]string{"decision": input.Decision}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) secretSets(w http.ResponseWriter, r *http.Request) {
	sets, err := a.store.ListSecretSets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list secret sets")
		return
	}
	writeJSON(w, http.StatusOK, sets)
}

func (a *API) upsertSecretSet(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Values map[string]string `json:"values"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Values) == 0 {
		writeError(w, http.StatusBadRequest, "name and values are required")
		return
	}
	ciphertext, err := a.vault.Encrypt(input.Values)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encrypt secret set")
		return
	}
	id, err := a.store.UpsertSecretSet(r.Context(), input.ID, input.Name, ciphertext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save secret set")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "vault.secret_set.write", "secret_set", id, map[string]any{"name": input.Name, "keys": mapKeys(input.Values)}, clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": input.Name})
}

func (a *API) deleteSecretSet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "secretID")
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Q("DELETE FROM secret_sets WHERE id=?"), id)
	if err != nil {
		writeError(w, http.StatusConflict, "secret set is in use")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "secret set not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) deploymentTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := a.store.ListDeploymentTargets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list deployment targets")
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

func (a *API) createDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProjectID    string                 `json:"project_id"`
		ServerID     string                 `json:"server_id"`
		SecretSetID  string                 `json:"secret_set_id"`
		Environment  string                 `json:"environment"`
		Repository   string                 `json:"repository"`
		GitRef       string                 `json:"git_ref"`
		ComposeFile  string                 `json:"compose_file"`
		WorkingDir   string                 `json:"working_dir"`
		BuildMode    string                 `json:"build_mode"`
		HealthChecks []protocol.HealthCheck `json:"health_checks"`
		ReleaseRoot  string                 `json:"release_root"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ProjectID == "" || input.ServerID == "" || strings.TrimSpace(input.Environment) == "" || strings.TrimSpace(input.Repository) == "" {
		writeError(w, http.StatusBadRequest, "project_id, server_id, environment, and repository are required")
		return
	}
	if input.BuildMode != "" && input.BuildMode != "build" && input.BuildMode != "pull" {
		writeError(w, http.StatusBadRequest, "build_mode must be build or pull")
		return
	}
	checks, _ := json.Marshal(input.HealthChecks)
	target, err := a.store.CreateDeploymentTarget(r.Context(), store.DeploymentTarget{ProjectID: input.ProjectID, ServerID: input.ServerID, SecretSetID: input.SecretSetID, Environment: strings.TrimSpace(input.Environment), Repository: strings.TrimSpace(input.Repository), GitRef: input.GitRef, ComposeFile: input.ComposeFile, WorkingDir: input.WorkingDir, BuildMode: input.BuildMode, HealthChecks: string(checks), ReleaseRoot: input.ReleaseRoot})
	if err != nil {
		if databaseConflict(err) {
			writeError(w, http.StatusConflict, "deployment target already exists")
		} else {
			writeError(w, http.StatusBadRequest, "could not create deployment target")
		}
		return
	}
	writeJSON(w, http.StatusCreated, target)
}

func (a *API) deployments(w http.ResponseWriter, r *http.Request) {
	deployments, err := a.store.ListDeployments(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list deployments")
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (a *API) startDeployment(w http.ResponseWriter, r *http.Request) {
	target, err := a.store.DeploymentTarget(r.Context(), chi.URLParam(r, "targetID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	var input struct {
		CommitRef string `json:"commit_ref"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.CommitRef == "" {
		input.CommitRef = target.GitRef
	}
	deployment, err := a.store.CreateDeployment(r.Context(), target.ID, input.CommitRef)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create deployment")
		return
	}
	environment := map[string]string{}
	if target.SecretSetID != "" {
		ciphertext, err := a.store.SecretCiphertext(r.Context(), target.SecretSetID)
		if err != nil || a.vault.Decrypt(ciphertext, &environment) != nil {
			writeError(w, http.StatusInternalServerError, "could not decrypt deployment secrets")
			return
		}
	}
	var checks []protocol.HealthCheck
	_ = json.Unmarshal([]byte(target.HealthChecks), &checks)
	command := protocol.DeployCommand{DeploymentID: deployment.ID, TargetID: target.ID, Repository: target.Repository, CommitRef: input.CommitRef, ComposeFile: target.ComposeFile, WorkingDir: target.WorkingDir, BuildMode: target.BuildMode, ReleaseRoot: target.ReleaseRoot, Environment: environment, HealthChecks: checks}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect deployment operation")
		return
	}
	operationID, err := a.store.QueueEncryptedOperation(r.Context(), target.ServerID, "deploy.start", ciphertext, "deploy:"+deployment.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue deployment")
		return
	}
	_ = a.store.AttachDeploymentOperation(r.Context(), deployment.ID, operationID)
	a.gateway.Wake(target.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "deployment.start", "deployment", deployment.ID, map[string]string{"target_id": target.ID, "commit_ref": input.CommitRef}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment": deployment, "operation_id": operationID})
}

func (a *API) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	target, err := a.store.DeploymentTarget(r.Context(), chi.URLParam(r, "targetID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	deployment, err := a.store.CreateDeployment(r.Context(), target.ID, "rollback")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create rollback")
		return
	}
	command := protocol.RollbackCommand{DeploymentID: deployment.ID, TargetID: target.ID, ReleaseRoot: target.ReleaseRoot, ComposeFile: target.ComposeFile, WorkingDir: target.WorkingDir}
	operationID, err := a.store.QueueOperation(r.Context(), target.ServerID, "deploy.rollback", command, "rollback:"+deployment.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue rollback")
		return
	}
	_ = a.store.AttachDeploymentOperation(r.Context(), deployment.ID, operationID)
	a.gateway.Wake(target.ServerID)
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment": deployment, "operation_id": operationID})
}

func (a *API) alerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := a.store.ListAlerts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list alerts")
		return
	}
	writeJSON(w, http.StatusOK, alerts)
}

func (a *API) acknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Q("UPDATE alerts SET acknowledged_at=? WHERE id=?"), time.Now().UTC(), chi.URLParam(r, "alertID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not acknowledge alert")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "alert not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type auditEntry struct {
	ID           string          `db:"id" json:"id"`
	Action       string          `db:"action" json:"action"`
	ResourceType string          `db:"resource_type" json:"resource_type"`
	ResourceID   string          `db:"resource_id" json:"resource_id"`
	Detail       json.RawMessage `db:"-" json:"detail"`
	DetailText   string          `db:"detail" json:"-"`
	IPAddress    string          `db:"ip_address" json:"ip_address"`
	OccurredAt   time.Time       `db:"occurred_at" json:"occurred_at"`
}

func (a *API) auditLog(w http.ResponseWriter, r *http.Request) {
	var entries []auditEntry
	err := a.store.DB.SelectContext(r.Context(), &entries, "SELECT id,action,resource_type,resource_id,detail,ip_address,occurred_at FROM audit_log ORDER BY occurred_at DESC LIMIT 500")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list audit log")
		return
	}
	for index := range entries {
		entries[index].Detail = json.RawMessage(entries[index].DetailText)
	}
	writeJSON(w, http.StatusOK, entries)
}

func validGitRemote(remote string) bool {
	if strings.HasPrefix(remote, "git@") || strings.HasPrefix(remote, "ssh://") {
		return true
	}
	parsed, err := url.Parse(remote)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func repositoryName(remote string) string {
	cleaned := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	cleaned = strings.TrimSuffix(cleaned, "/")
	name := filepath.Base(strings.ReplaceAll(cleaned, ":", "/"))
	if name == "." || name == "" {
		return "Imported project"
	}
	return name
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

var _ = sql.ErrNoRows
var _ = fmt.Sprintf
