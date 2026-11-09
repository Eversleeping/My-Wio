package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) manageWorkspaceGeminiSkill(w http.ResponseWriter, r *http.Request) {
	var input struct {
		protocol.GeminiSkillManageCommand
		RequestID string `json:"request_id"`
	}
	if !decodeJSONLimit(w, r, &input, 8<<10) {
		return
	}
	if _, err := uuid.Parse(input.RequestID); err != nil {
		writeError(w, 400, "invalid skill request ID")
		return
	}
	if err := protocol.ValidateGeminiSkillManagement(input.GeminiSkillManageCommand); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	workspace, err := a.store.Workspace(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load workspace")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, 409, "server is offline")
		return
	}
	if !buildinfo.SupportsGeminiSkillManagement(server.AgentVersion, server.GeminiVersion) {
		writeError(w, 409, "skill management requires Agent 0.2.88 and Gemini CLI 0.66.0 or newer")
		return
	}
	op, err := a.store.QueueGeminiSkillManagement(r.Context(), workspace, input.GeminiSkillManageCommand, input.RequestID)
	if err != nil {
		writeStoreError(w, err, "could not queue request")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "gemini.skills."+input.Action, "workspace", workspace.ID, map[string]string{"operation_id": op, "scope": input.Scope, "name": input.Name}, clientIP(r))
	a.gateway.Wake(workspace.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": op})
}

func (a *API) refreshWorkspaceGeminiSkills(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	if !buildinfo.SupportsGeminiSkills(server.AgentVersion, server.GeminiVersion) {
		writeError(w, http.StatusConflict, "Gemini skills require Wio Agent 0.2.83 and Gemini CLI 0.66.0 or newer")
		return
	}
	op, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, "gemini.skills.list", protocol.GeminiSkillsCommand{Workspace: workspace.Path}, "gemini-skills:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Gemini skill discovery")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": op})
}

func (a *API) workspaceGeminiSkillsResult(w http.ResponseWriter, r *http.Request) {
	op, err := a.store.Operation(r.Context(), chi.URLParam(r, "operationID"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ((op.Kind != "gemini.skills.list" && op.Kind != "gemini.skills.manage") || op.WorkspaceID != chi.URLParam(r, "workspaceID"))) {
		writeError(w, http.StatusNotFound, "Gemini skill discovery not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Gemini skill discovery")
		return
	}
	response := map[string]any{"status": op.Status, "error": op.Result}
	if op.Status == "succeeded" {
		var result protocol.GeminiSkillsResult
		if json.Unmarshal(security.RedactJSON(json.RawMessage(op.ResultData)), &result) != nil || result.Skills == nil || len(result.Skills) > 500 {
			writeError(w, http.StatusInternalServerError, "invalid Gemini skill discovery result")
			return
		}
		response["skills"] = result.Skills
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}
