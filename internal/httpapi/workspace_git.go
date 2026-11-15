package httpapi

import (
	"database/sql"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *API) workspaceGit(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.store.WorkspaceGitSnapshot(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace Git information")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (a *API) refreshWorkspaceGit(w http.ResponseWriter, r *http.Request) {
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
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	command := protocol.GitWorkspaceInspectCommand{WorkspaceID: workspace.ID, Path: workspace.Path, CommitLimit: 50}
	if err := a.store.BeginWorkspaceGitRefresh(r.Context(), workspace.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start workspace Git refresh")
		return
	}
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpGitWorkspaceInspect, command, "git-inspect:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		_ = a.store.FailWorkspaceGitRefresh(r.Context(), workspace.ID, "could not queue workspace Git refresh")
		writeError(w, http.StatusInternalServerError, "could not queue workspace Git refresh")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "git.workspace.refresh", "workspace", workspace.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) workspaceGitCommits(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.store.WorkspaceGitSnapshot(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load commits")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	end := offset + limit
	if end > len(snapshot.Data.Commits) {
		end = len(snapshot.Data.Commits)
	}
	commits := []protocol.GitCommit{}
	if offset < len(snapshot.Data.Commits) {
		commits = snapshot.Data.Commits[offset:end]
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": commits, "offset": offset, "limit": limit, "has_more": end < len(snapshot.Data.Commits) || snapshot.Data.HasMore})
}

func (a *API) workspaceGitCommit(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.store.WorkspaceGitSnapshot(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load commit")
		return
	}
	sha := strings.TrimSpace(chi.URLParam(r, "sha"))
	for _, commit := range snapshot.Data.Commits {
		if commit.SHA == sha {
			writeJSON(w, http.StatusOK, commit)
			return
		}
	}
	writeError(w, http.StatusNotFound, "commit not found in the current snapshot")
}

func (a *API) queueWorkspaceGitWrite(w http.ResponseWriter, r *http.Request, command protocol.GitWorkspaceWriteCommand) {
	workspace, err := a.store.Workspace(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	if workspace.ManagementMode != "managed" {
		writeError(w, http.StatusConflict, "workspace is observed and cannot be modified")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	command.WorkspaceID, command.Path = workspace.ID, workspace.Path
	// Check the write lock first: marking the snapshot as refreshing for a
	// request that is about to be rejected would corrupt its visible status.
	active, err := a.store.HasActiveWorkspaceWriteOperation(r.Context(), workspace.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check workspace write operations")
		return
	}
	if active {
		writeError(w, http.StatusConflict, store.ErrWorkspaceWriteActive.Error())
		return
	}
	previous, err := a.store.WorkspaceGitSnapshot(r.Context(), workspace.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace Git status")
		return
	}
	if err := a.store.BeginWorkspaceGitRefresh(r.Context(), workspace.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start workspace Git operation")
		return
	}
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpGitWorkspaceWrite, command, "git-write:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, true)
	if errors.Is(err, store.ErrWorkspaceWriteActive) {
		// A concurrent write won the race after the check above. Nothing ran
		// for this request, so put the snapshot back instead of failing it.
		_ = a.store.RestoreWorkspaceGitRefresh(r.Context(), previous)
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		_ = a.store.FailWorkspaceGitRefresh(r.Context(), workspace.ID, "could not queue Git operation")
		writeError(w, http.StatusInternalServerError, "could not queue Git operation")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "git."+command.Action, "workspace", workspace.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) createGitBranch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Start string `json:"start_point"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "branch.create", Branch: strings.TrimSpace(in.Name), StartPoint: strings.TrimSpace(in.Start)})
}

func (a *API) renameGitBranch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "branch.rename", Branch: gitBranchURLParam(r), NewBranch: strings.TrimSpace(in.Name)})
}

func (a *API) deleteGitBranch(w http.ResponseWriter, r *http.Request) {
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "branch.delete", Branch: gitBranchURLParam(r), Force: r.URL.Query().Get("force") == "true"})
}

func gitBranchURLParam(r *http.Request) string {
	raw := chi.URLParam(r, "branch")
	if raw == "" {
		raw = chi.URLParam(r, "*")
	}
	branch, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return branch
}

func (a *API) checkoutGit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref    string `json:"ref"`
		Detach bool   `json:"detach"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "checkout", Ref: strings.TrimSpace(in.Ref), Detach: in.Detach})
}

func (a *API) addGitRemote(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, URL string }
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := gitRemoteCredentialError(in.URL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "remote.add", Remote: strings.TrimSpace(in.Name), URL: strings.TrimSpace(in.URL)})
}

func (a *API) setGitRemote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := gitRemoteCredentialError(in.URL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "remote.set-url", Remote: chi.URLParam(r, "remote"), URL: strings.TrimSpace(in.URL)})
}

func (a *API) deleteGitRemote(w http.ResponseWriter, r *http.Request) {
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "remote.remove", Remote: chi.URLParam(r, "remote")})
}

func (a *API) fetchGit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Remote string `json:"remote"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "fetch", Remote: strings.TrimSpace(in.Remote)})
}

func (a *API) pullGit(w http.ResponseWriter, r *http.Request) {
	var in struct{ Remote, Branch string }
	if !decodeJSON(w, r, &in) {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "pull", Remote: strings.TrimSpace(in.Remote), Branch: strings.TrimSpace(in.Branch)})
}

func (a *API) pushGit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Remote, Ref string
		SetUpstream bool `json:"set_upstream"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "push", Remote: strings.TrimSpace(in.Remote), Ref: strings.TrimSpace(in.Ref), SetUpstream: in.SetUpstream})
}

type gitChangeSelectionInput struct {
	Paths         []string `json:"paths"`
	All           bool     `json:"all"`
	IncludeStaged bool     `json:"include_staged"`
}

func decodeGitChangeSelection(w http.ResponseWriter, r *http.Request) (gitChangeSelectionInput, bool) {
	var in gitChangeSelectionInput
	if !decodeJSON(w, r, &in) {
		return in, false
	}
	if !in.All && len(in.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "at least one changed path or all=true is required")
		return in, false
	}
	if len(in.Paths) > 512 {
		writeError(w, http.StatusBadRequest, "too many changed paths")
		return in, false
	}
	return in, true
}

func (a *API) stageGitChanges(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeGitChangeSelection(w, r)
	if !ok {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "stage", Paths: in.Paths, All: in.All})
}

func (a *API) unstageGitChanges(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeGitChangeSelection(w, r)
	if !ok {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "unstage", Paths: in.Paths, All: in.All})
}

func (a *API) discardGitChanges(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeGitChangeSelection(w, r)
	if !ok {
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "discard", Paths: in.Paths, All: in.All, IncludeStaged: in.IncludeStaged})
}

func (a *API) commitGitChanges(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message string `json:"message"`
		All     bool   `json:"all"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		writeError(w, http.StatusBadRequest, "commit message is required")
		return
	}
	if len(in.Message) > 20*1024 {
		writeError(w, http.StatusBadRequest, "commit message is too long")
		return
	}
	a.queueWorkspaceGitWrite(w, r, protocol.GitWorkspaceWriteCommand{Action: "commit", Message: in.Message, All: in.All})
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
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
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
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpGitWorktreeCreate, command, "git-worktree:"+command.TargetWorkspaceID, store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, true)
	if err != nil {
		writeStoreError(w, err, "could not queue worktree creation")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "git.worktree.create", "workspace", workspace.ID, map[string]string{"operation_id": operationID, "target_workspace_id": command.TargetWorkspaceID, "branch": command.Branch}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "workspace_id": command.TargetWorkspaceID})
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
