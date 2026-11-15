package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	pathpkg "path"
	"strings"
)

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
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpWorkspaceFiles, command, "workspace-files:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
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
	hash := ""
	if preview.Status == "succeeded" && preview.Truncated == 0 && preview.Encoding == "" {
		hash = workspaceContentHash(preview.Content)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": preview.WorkspaceID, "path": preview.Path, "content": preview.Content, "mime_type": preview.MIMEType, "encoding": preview.Encoding, "hash": hash, "size": preview.Size, "truncated": preview.Truncated != 0, "status": preview.Status, "error": preview.Error, "requested_at": preview.RequestedAt, "updated_at": preview.UpdatedAt})
}

func (a *API) requestWorkspaceFilePreview(w http.ResponseWriter, r *http.Request) {
	a.requestWorkspacePreview(w, r, false)
}

// Conversation images use operation-scoped results so multiple thumbnails can
// load without replacing the file panel's current workspace snapshot.
func (a *API) requestWorkspaceImagePreview(w http.ResponseWriter, r *http.Request) {
	a.requestWorkspacePreview(w, r, true)
}

func (a *API) requestWorkspacePreview(w http.ResponseWriter, r *http.Request, imageOnly bool) {
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
	kind := protocol.OpWorkspaceFilePreview
	if imageOnly {
		kind = protocol.OpWorkspaceImagePreview
	} else {
		if err := a.store.BeginWorkspaceFilePreview(r.Context(), workspace.ID, path); err != nil {
			writeError(w, http.StatusInternalServerError, "could not start workspace file preview")
			return
		}
	}
	command := protocol.WorkspaceFilePreviewCommand{WorkspaceID: workspace.ID, Root: workspace.Path, Path: path}
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, kind, command, kind+":"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		if !imageOnly {
			_ = a.store.FailWorkspaceFilePreview(r.Context(), workspace.ID, path, "could not queue workspace file preview")
		}
		writeError(w, http.StatusInternalServerError, "could not queue workspace file preview")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, kind, "workspace", workspace.ID, map[string]string{"operation_id": operationID, "path": path}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "path": path})
}

func (a *API) workspaceChanges(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceID")
	if _, err := a.store.Workspace(r.Context(), workspaceID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	snapshot, err := a.store.WorkspaceChangeSnapshot(r.Context(), workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"workspace_id": workspaceID, "changes": []protocol.WorkspaceChange{}, "status": "idle", "error": "", "requested_at": nil, "updated_at": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace changes")
		return
	}
	var changes []protocol.WorkspaceChange
	if err := json.Unmarshal([]byte(snapshot.Changes), &changes); err != nil {
		writeError(w, http.StatusInternalServerError, "workspace change snapshot is invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": workspaceID, "changes": changes, "status": snapshot.Status, "error": snapshot.Error, "requested_at": snapshot.RequestedAt, "updated_at": snapshot.UpdatedAt})
}

func (a *API) refreshWorkspaceChanges(w http.ResponseWriter, r *http.Request) {
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
	if err := a.store.BeginWorkspaceChangeScan(r.Context(), workspace.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start workspace change scan")
		return
	}
	command := protocol.WorkspaceChangesCommand{WorkspaceID: workspace.ID, Path: workspace.Path}
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpWorkspaceChanges, command, "workspace-changes:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		_ = a.store.FailWorkspaceChangeScan(r.Context(), workspace.ID, "could not queue workspace change scan")
		writeError(w, http.StatusInternalServerError, "could not queue workspace change scan")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace.changes.scan", "workspace", workspace.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) workspaceDiffPreview(w http.ResponseWriter, r *http.Request) {
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
	preview, err := a.store.WorkspaceDiffPreview(r.Context(), workspaceID, path)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"workspace_id": workspaceID, "path": path, "content": "", "additions": 0, "deletions": 0, "binary": false, "truncated": false, "status": "idle", "error": "", "requested_at": nil, "updated_at": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace diff preview")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": preview.WorkspaceID, "path": preview.Path, "content": preview.Content, "additions": preview.Additions, "deletions": preview.Deletions, "binary": preview.Binary != 0, "truncated": preview.Truncated != 0, "status": preview.Status, "error": preview.Error, "requested_at": preview.RequestedAt, "updated_at": preview.UpdatedAt})
}

func (a *API) requestWorkspaceDiffPreview(w http.ResponseWriter, r *http.Request) {
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
	snapshot, err := a.store.WorkspaceChangeSnapshot(r.Context(), workspace.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && snapshot.Status != "succeeded" {
		writeError(w, http.StatusConflict, "workspace changes must be refreshed before opening a diff")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace changes")
		return
	}
	var changes []protocol.WorkspaceChange
	if err := json.Unmarshal([]byte(snapshot.Changes), &changes); err != nil {
		writeError(w, http.StatusInternalServerError, "workspace change snapshot is invalid")
		return
	}
	oldPath := ""
	found := false
	for _, change := range changes {
		if change.Path == path {
			oldPath, found = change.OldPath, true
			break
		}
	}
	if !found {
		writeError(w, http.StatusConflict, "file is not modified")
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
	if err := a.store.BeginWorkspaceDiffPreview(r.Context(), workspace.ID, path); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start workspace diff preview")
		return
	}
	command := protocol.WorkspaceDiffCommand{WorkspaceID: workspace.ID, Root: workspace.Path, Path: path, OldPath: oldPath}
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpWorkspaceDiffPreview, command, "workspace-diff-preview:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		_ = a.store.FailWorkspaceDiffPreview(r.Context(), workspace.ID, path, "could not queue workspace diff preview")
		writeError(w, http.StatusInternalServerError, "could not queue workspace diff preview")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace.diff.preview", "workspace", workspace.ID, map[string]string{"operation_id": operationID, "path": path}, clientIP(r))
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
