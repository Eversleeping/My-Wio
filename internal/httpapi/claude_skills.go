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

func (a *API) refreshWorkspaceClaudeSkills(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	if server.Status != "online" || workspace.Status != "ready" {
		writeError(w, 409, "skill discovery requires an online server and ready workspace")
		return
	}
	if !buildinfo.SupportsClaudeSkills(server.AgentVersion) {
		writeError(w, 409, "Claude skills require Agent "+buildinfo.MinimumClaudeSkillsVersion+" or newer")
		return
	}
	if server.ClaudeReady != 1 {
		writeError(w, 409, "Claude Code is not installed or ready")
		return
	}
	op, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, "claude.skills.list", protocol.ClaudeSkillsCommand{Workspace: workspace.Path}, "claude-skills:"+workspace.ID+":"+store.NewID(), store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		writeError(w, 500, "could not queue Claude skill discovery")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": op})
}

func (a *API) workspaceClaudeSkillsResult(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	op, err := a.store.Operation(r.Context(), chi.URLParam(r, "operationID"))
	if errors.Is(err, sql.ErrNoRows) || err == nil && (op.Kind != "claude.skills.list" || op.WorkspaceID != chi.URLParam(r, "workspaceID")) {
		writeError(w, 404, "Claude skill discovery not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load Claude skill discovery")
		return
	}
	response := map[string]any{"status": op.Status}
	if op.Status == "failed" || op.Status == "interrupted" || op.Status == "cancelled" || op.Status == "canceled" {
		response["error"] = "Claude skill discovery failed; check the CLI installation and retry"
	}
	if op.Status == "succeeded" {
		var result protocol.ClaudeSkillsResult
		if json.Unmarshal(security.RedactJSON(json.RawMessage(op.ResultData)), &result) != nil || result.Skills == nil || len(result.Skills) > 500 {
			writeError(w, 500, "invalid Claude skill discovery result")
			return
		}
		for _, skill := range result.Skills {
			if !protocol.ValidClaudeSkillName(skill.Name) || len(skill.Description) > 4096 || len(skill.ArgumentHint) > 256 {
				writeError(w, 500, "invalid Claude skill discovery result")
				return
			}
		}
		response["skills"] = result.Skills
	}
	writeJSON(w, 200, response)
}
