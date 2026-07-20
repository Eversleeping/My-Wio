package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/store"
)

const (
	projectDeletionMetadataOnly = "metadata-only"
	projectDeletionManagedFiles = "managed-files"
)

func (a *API) projectDeletionPlan(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	plan, err := a.store.ProjectDeletionPlan(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build project deletion plan")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &input) {
		return
	}
	mode := strings.TrimSpace(input.Mode)
	if queryMode := strings.TrimSpace(r.URL.Query().Get("mode")); mode == "" {
		mode = queryMode
	}
	if mode == "" {
		a.deleteProjectLegacy(w, r)
		return
	}
	if mode != projectDeletionMetadataOnly && mode != projectDeletionManagedFiles {
		writeError(w, http.StatusBadRequest, "mode must be metadata-only or managed-files")
		return
	}
	projectID := chi.URLParam(r, "projectID")
	plan, err := a.store.ProjectDeletionPlan(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build project deletion plan")
		return
	}
	if mode == projectDeletionMetadataOnly {
		if !plan.CanDeleteMetadata {
			writeDeletionBlocked(w, plan)
			return
		}
		deleted, err := a.store.DeleteProjectMetadata(r.Context(), projectID)
		if errors.Is(err, store.ErrProjectDeletionBlocked) {
			latest, latestErr := a.store.ProjectDeletionPlan(r.Context(), projectID)
			if latestErr == nil {
				writeDeletionBlocked(w, latest)
				return
			}
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not delete project metadata")
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		a.auditProjectDeletion(r, plan, mode, nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": mode, "remote_deleted": false})
		return
	}
	if !plan.CanDeleteManagedFiles {
		writeDeletionBlocked(w, plan)
		return
	}
	operationIDs, deleted, err := a.store.QueueProjectManagedDeletion(r.Context(), projectID)
	if errors.Is(err, store.ErrProjectDeletionBlocked) {
		latest, latestErr := a.store.ProjectDeletionPlan(r.Context(), projectID)
		if latestErr == nil {
			writeDeletionBlocked(w, latest)
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue managed project deletion")
		return
	}
	a.auditProjectDeletion(r, plan, mode, operationIDs)
	if deleted {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": mode, "remote_deleted": false})
		return
	}
	servers := make(map[string]struct{})
	for _, workspace := range plan.Workspaces {
		if workspace.ManagementMode == "managed" {
			servers[workspace.ServerID] = struct{}{}
		}
	}
	for serverID := range servers {
		a.gateway.Wake(serverID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "mode": mode, "status": "deleting", "operation_ids": operationIDs, "remote_deleted": false})
}

func writeDeletionBlocked(w http.ResponseWriter, plan store.ProjectDeletionPlan) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": "project deletion is blocked", "plan": plan})
}

func (a *API) auditProjectDeletion(r *http.Request, plan store.ProjectDeletionPlan, mode string, operationIDs []string) {
	details := map[string]string{
		"name":               plan.ProjectName,
		"mode":               mode,
		"remote_preserved":   "true",
		"workspace_count":    strconv.Itoa(plan.WorkspaceCount),
		"managed_workspaces": strconv.Itoa(plan.ManagedWorkspaceCount),
	}
	if len(operationIDs) > 0 {
		details["operation_ids"] = strings.Join(operationIDs, ",")
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "project.delete."+strings.ReplaceAll(mode, "-", "_"), "project", plan.ProjectID, details, clientIP(r))
}
