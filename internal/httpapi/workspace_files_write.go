package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func workspaceContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}

func (a *API) writeWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path          string  `json:"path"`
		Content       *string `json:"content"`
		ContentBase64 *string `json:"content_base64"`
		ExpectedHash  string  `json:"expected_hash"`
	}
	if !decodeJSONLimit(w, r, &input, 8<<20) {
		return
	}
	path := strings.ReplaceAll(strings.TrimSpace(input.Path), "\\", "/")
	_, valid := normalizeWorkspaceFilePath(path)
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			valid = false
		}
	}
	if !valid || strings.ContainsAny(path, ":\x00") || (input.Content == nil) == (input.ContentBase64 == nil) {
		writeError(w, http.StatusBadRequest, "a workspace file path and exactly one content field are required")
		return
	}
	var content []byte
	if input.Content != nil {
		content = []byte(*input.Content)
	} else {
		var err error
		content, err = base64.StdEncoding.DecodeString(*input.ContentBase64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid file encoding")
			return
		}
	}
	if len(content) > protocol.MaxWorkspaceFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds the 1 MiB limit")
		return
	}
	create := r.Method == http.MethodPost
	if !create {
		hash, err := hex.DecodeString(input.ExpectedHash)
		if err != nil || len(hash) != sha256.Size {
			writeError(w, http.StatusBadRequest, "the original file hash is required")
			return
		}
	}
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
	if err != nil || server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	if !buildinfo.SupportsWorkspaceFileWrite(server.AgentVersion) {
		writeError(w, http.StatusConflict, "upgrade the server Agent to 0.2.56 or later to add or edit files")
		return
	}
	command := protocol.WorkspaceFileWriteCommand{WorkspaceID: workspace.ID, Root: workspace.Path, Path: path, Content: content, Create: create, ExpectedHash: strings.ToLower(input.ExpectedHash)}
	operationID, err := a.store.QueueResourceOperation(r.Context(), server.ID, "workspace.file.write", command, "workspace-file-write:"+store.NewID(), store.OperationResource{WorkspaceID: workspace.ID}, true)
	if errors.Is(err, store.ErrWorkspaceWriteActive) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue file write")
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "workspace.file.write", "workspace", workspace.ID, map[string]any{"operation_id": operationID, "path": path, "create": create}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

// Return progress without exposing the operation payload (which may contain secrets).
func (a *API) operationStatus(w http.ResponseWriter, r *http.Request) {
	operation, err := a.store.Operation(r.Context(), chi.URLParam(r, "operationID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "operation not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load operation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": operation.ID, "status": operation.Status, "result": operation.Result})
}
