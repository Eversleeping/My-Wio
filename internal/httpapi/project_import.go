package httpapi

import (
	"database/sql"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"strings"
)

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
	input.ServerID = strings.TrimSpace(input.ServerID)
	input.Name = strings.TrimSpace(input.Name)
	input.RemoteURL = strings.TrimSpace(input.RemoteURL)
	input.Destination = strings.TrimSpace(input.Destination)
	if input.ServerID == "" || input.RemoteURL == "" {
		writeError(w, http.StatusBadRequest, "server_id and remote_url are required")
		return
	}
	if err := gitRemoteCredentialError(input.RemoteURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validGitRemote(input.RemoteURL) {
		writeError(w, http.StatusBadRequest, "remote_url must use HTTPS or SSH")
		return
	}
	if input.Name == "" {
		input.Name = repositoryName(input.RemoteURL)
	}
	project, operationID, err := a.store.QueueProjectImport(r.Context(), input.ServerID, input.Name, input.RemoteURL, input.Destination)
	if errors.Is(err, store.ErrProjectAlreadyOnServer) {
		writeError(w, http.StatusConflict, "project already exists on target server")
		return
	}
	if err != nil {
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
	operationID, err := a.store.QueueResourceOperation(r.Context(), server.ID, protocol.OpGitImport, command, "git-import-retry:"+project.ID+":"+store.NewID(), store.OperationResource{ProjectID: project.ID}, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Git import retry")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.import.retry", "project", project.ID, map[string]string{"server_id": server.ID, "operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *API) deleteProjectLegacy(w http.ResponseWriter, r *http.Request) {
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
	plan, err := a.store.ProjectDeletionPlan(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check project dependencies")
		return
	}
	if !plan.CanDeleteMetadata {
		writeDeletionBlocked(w, plan)
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
	operationID, err := a.store.QueueProjectDiscovery(r.Context(), server.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue project discovery")
		return
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.inventory.scan", "server", server.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}
