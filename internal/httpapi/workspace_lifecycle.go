package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

const (
	workspaceDisplayNameLimit = 200
	workspacePathLimit        = 4096
)

var windowsAbsolutePath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func (a *API) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DisplayName *string `json:"display_name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.DisplayName == nil {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	name := strings.TrimSpace(*input.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > workspaceDisplayNameLimit || containsControlRune(name) {
		writeError(w, http.StatusBadRequest, "valid workspace display_name is required")
		return
	}
	workspace, err := a.store.UpdateWorkspaceDisplayName(r.Context(), chi.URLParam(r, "workspaceID"), name)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update workspace")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace.update", "workspace", workspace.ID, map[string]string{"display_name": name}, clientIP(r))
	writeJSON(w, http.StatusOK, workspace)
}

func (a *API) moveWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	workspace, server, ok := a.lifecycleWorkspace(w, r, "move", input.Path)
	if !ok {
		return
	}
	command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, Action: "move", SourcePath: workspace.Path, TargetPath: strings.TrimSpace(input.Path), WorkspaceKind: workspace.Kind}
	a.queueWorkspaceLifecycle(w, r, workspace, server, command)
}

func (a *API) copyWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerID string `json:"server_id"`
		Path     string `json:"path"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	workspace, sourceServer, ok := a.lifecycleWorkspace(w, r, "copy", input.Path)
	if !ok {
		return
	}
	targetServerID := strings.TrimSpace(input.ServerID)
	if targetServerID == "" {
		targetServerID = workspace.ServerID
	}
	if targetServerID != workspace.ServerID {
		targetServer, err := a.store.Server(r.Context(), targetServerID)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "target server not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load target server")
			return
		}
		if targetServer.Status != "online" || len(store.ManagedRoots(targetServer)) == 0 {
			writeError(w, http.StatusConflict, "target server is offline or has no managed roots")
			return
		}
		command, blocker, err := a.crossServerCloneCommand(r, workspace, strings.TrimSpace(input.Path))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not inspect source workspace")
			return
		}
		if blocker != "" {
			writeError(w, http.StatusConflict, blocker)
			return
		}
		operationID, err := a.store.QueueCrossServerWorkspaceClone(r.Context(), workspace, targetServer.ID, command)
		if errors.Is(err, store.ErrWorkspaceWriteActive) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not queue cross-server workspace copy")
			return
		}
		a.gateway.Wake(targetServer.ID)
		_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace.copy.cross_server", "workspace", workspace.ID, map[string]string{"operation_id": operationID, "target_workspace_id": command.WorkspaceID, "target_server_id": targetServer.ID, "target_path": command.Destination}, clientIP(r))
		writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "workspace_id": command.WorkspaceID})
		return
	}
	command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, TargetWorkspaceID: store.NewID(), ProjectID: workspace.ProjectID, Action: "copy", SourcePath: workspace.Path, TargetPath: strings.TrimSpace(input.Path), WorkspaceKind: workspace.Kind}
	a.queueWorkspaceLifecycle(w, r, workspace, sourceServer, command)
}

func (a *API) crossServerCloneCommand(r *http.Request, workspace store.Workspace, destination string) (protocol.GitWorkspaceCloneCommand, string, error) {
	snapshot, err := a.store.WorkspaceGitSnapshot(r.Context(), workspace.ID)
	if err != nil {
		return protocol.GitWorkspaceCloneCommand{}, "", err
	}
	status := snapshot.Data.Status
	switch {
	case snapshot.Status != "succeeded":
		return protocol.GitWorkspaceCloneCommand{}, "source Git status must be refreshed before cross-server copy", nil
	case snapshot.UpdatedAt == nil || time.Since(*snapshot.UpdatedAt) > store.ServerOnlineGracePeriod:
		return protocol.GitWorkspaceCloneCommand{}, "source Git status is stale and must be refreshed before cross-server copy", nil
	case workspace.Dirty != 0 || status.Dirty || status.Staged != 0 || status.Unstaged != 0 || status.Untracked != 0:
		return protocol.GitWorkspaceCloneCommand{}, "source workspace must be clean before cross-server copy", nil
	case status.Detached || status.Unborn || status.Branch == "" || status.Head == "":
		return protocol.GitWorkspaceCloneCommand{}, "source workspace must have a checked-out branch and commit", nil
	case status.Upstream == "":
		return protocol.GitWorkspaceCloneCommand{}, "source branch must have an upstream before cross-server copy", nil
	case status.Ahead != 0:
		return protocol.GitWorkspaceCloneCommand{}, "source branch has unpushed local commits", nil
	}
	remoteName := strings.SplitN(status.Upstream, "/", 2)[0]
	fetchURL := ""
	for _, remote := range snapshot.Data.Remotes {
		if remote.Name == remoteName && len(remote.FetchURLs) != 0 {
			fetchURL = strings.TrimSpace(remote.FetchURLs[0])
			break
		}
	}
	if fetchURL == "" || !validGitRemote(fetchURL) {
		return protocol.GitWorkspaceCloneCommand{}, "source upstream has no usable HTTPS or SSH fetch remote", nil
	}
	project, err := a.store.Project(r.Context(), workspace.ProjectID)
	if err != nil {
		return protocol.GitWorkspaceCloneCommand{}, "", err
	}
	configured := []string{project.RemoteURL}
	remotes, err := a.store.ProjectRemotes(r.Context(), workspace.ProjectID)
	if err != nil {
		return protocol.GitWorkspaceCloneCommand{}, "", err
	}
	for _, remote := range remotes {
		if remote.Status == "ready" {
			configured = append(configured, remote.FetchURL)
		}
	}
	approved := false
	for _, candidate := range configured {
		if sameWorkspaceRemote(candidate, fetchURL) {
			approved = true
			break
		}
	}
	if !approved {
		return protocol.GitWorkspaceCloneCommand{}, "source upstream is not registered as a project fetch remote", nil
	}
	name := strings.TrimSpace(workspace.DisplayName)
	if name == "" {
		name = workspace.ProjectName
	}
	return protocol.GitWorkspaceCloneCommand{WorkspaceID: store.NewID(), ProjectID: workspace.ProjectID, Name: name, Destination: destination, RemoteURL: fetchURL, Branch: status.Branch, ExpectedHead: status.Head}, "", nil
}

func sameWorkspaceRemote(first, second string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(strings.TrimSuffix(value, "/"))
		value = strings.TrimSuffix(value, ".git")
		return strings.ToLower(value)
	}
	return normalize(first) != "" && normalize(first) == normalize(second)
}

func (a *API) workspaceDeletionPlan(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	plan, err := a.store.WorkspaceDeletionPlan(r.Context(), chi.URLParam(r, "workspaceID"), force)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build workspace deletion plan")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (a *API) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
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
	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	switch mode {
	case "metadata":
		if err := a.store.RemoveWorkspaceRecord(r.Context(), workspace.ID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace.remove_record", "workspace", workspace.ID, map[string]string{"path": workspace.Path}, clientIP(r))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case "files":
		force := r.URL.Query().Get("force") == "true"
		plan, err := a.store.WorkspaceDeletionPlan(r.Context(), workspace.ID, force)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not build workspace deletion plan")
			return
		}
		if !plan.CanDeleteFiles {
			writeError(w, http.StatusConflict, strings.Join(plan.Blockers, "; "))
			return
		}
		server, err := a.store.Server(r.Context(), workspace.ServerID)
		if err != nil || server.Status != "online" || len(store.ManagedRoots(server)) == 0 {
			writeError(w, http.StatusConflict, "server is offline or has no managed roots")
			return
		}
		command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, Action: "delete", SourcePath: workspace.Path, WorkspaceKind: workspace.Kind, Force: force}
		a.queueWorkspaceLifecycle(w, r, workspace, server, command)
	default:
		writeError(w, http.StatusBadRequest, "workspace delete mode must be metadata or files")
	}
}

func (a *API) lifecycleWorkspace(w http.ResponseWriter, r *http.Request, action, targetPath string) (store.Workspace, store.Server, bool) {
	workspace, err := a.store.Workspace(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return store.Workspace{}, store.Server{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return store.Workspace{}, store.Server{}, false
	}
	if workspace.ManagementMode != "managed" {
		writeError(w, http.StatusConflict, "workspace is observed and cannot be "+action+"d")
		return store.Workspace{}, store.Server{}, false
	}
	targetPath = strings.TrimSpace(targetPath)
	if !validAgentAbsolutePath(targetPath) || utf8.RuneCountInString(targetPath) > workspacePathLimit || containsControlRune(targetPath) {
		writeError(w, http.StatusBadRequest, "target path must be a valid absolute path")
		return store.Workspace{}, store.Server{}, false
	}
	if cleanAgentPath(targetPath) == cleanAgentPath(workspace.Path) {
		writeError(w, http.StatusBadRequest, "target path must differ from the current workspace path")
		return store.Workspace{}, store.Server{}, false
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil || server.Status != "online" || len(store.ManagedRoots(server)) == 0 {
		writeError(w, http.StatusConflict, "server is offline or has no managed roots")
		return store.Workspace{}, store.Server{}, false
	}
	return workspace, server, true
}

func (a *API) queueWorkspaceLifecycle(w http.ResponseWriter, r *http.Request, workspace store.Workspace, server store.Server, command protocol.GitWorkspaceLifecycleCommand) {
	operationID, err := a.store.QueueWorkspaceLifecycle(r.Context(), workspace, command)
	if errors.Is(err, store.ErrWorkspaceWriteActive) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue workspace "+command.Action)
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace."+command.Action, "workspace", workspace.ID, map[string]string{"operation_id": operationID, "target_workspace_id": command.TargetWorkspaceID, "target_path": command.TargetPath}, clientIP(r))
	response := map[string]string{"operation_id": operationID}
	if command.TargetWorkspaceID != "" {
		response["workspace_id"] = command.TargetWorkspaceID
	}
	writeJSON(w, http.StatusAccepted, response)
}

func containsControlRune(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func validAgentAbsolutePath(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || windowsAbsolutePath.MatchString(value)
}

func cleanAgentPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	return filepath.ToSlash(filepath.Clean(value))
}
