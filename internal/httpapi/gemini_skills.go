package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

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
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (op.Kind != "gemini.skills.list" || op.WorkspaceID != chi.URLParam(r, "workspaceID"))) {
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
