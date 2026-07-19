package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) threadCodexStatus(w http.ResponseWriter, r *http.Request) {
	a.writeCodexSnapshot(w, r, "thread", chi.URLParam(r, "threadID"), "status.snapshot", true)
}
func (a *API) threadGoal(w http.ResponseWriter, r *http.Request) {
	a.writeCodexSnapshot(w, r, "thread", chi.URLParam(r, "threadID"), "goal", true)
}
func (a *API) workspaceCodexMCP(w http.ResponseWriter, r *http.Request) {
	a.writeCodexSnapshot(w, r, "workspace", chi.URLParam(r, "workspaceID"), "mcp.list", false)
}
func (a *API) workspaceCodexSkills(w http.ResponseWriter, r *http.Request) {
	a.writeCodexSnapshot(w, r, "workspace", chi.URLParam(r, "workspaceID"), "skills.list", false)
}

func (a *API) writeCodexSnapshot(w http.ResponseWriter, r *http.Request, scopeType, scopeID, kind string, thread bool) {
	if !a.codexScopeExists(r, scopeType, scopeID) {
		writeError(w, http.StatusNotFound, scopeType+" not found")
		return
	}
	snapshot, err := a.store.CodexSnapshot(r.Context(), scopeType, scopeID, kind)
	if errors.Is(err, sql.ErrNoRows) {
		response := map[string]any{"scope_type": scopeType, "scope_id": scopeID, "kind": kind, "data": map[string]any{}, "supported": true, "status": "idle", "error": "", "reason": "", "requested_at": nil, "updated_at": nil}
		if thread && kind == "status.snapshot" {
			response["plan"] = map[string]any{"supported": false, "reason": "This Codex version does not expose a writable collaboration mode"}
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex snapshot")
		return
	}
	var data any = map[string]any{}
	if err := json.Unmarshal([]byte(snapshot.Data), &data); err != nil {
		writeError(w, http.StatusInternalServerError, "Codex snapshot is invalid")
		return
	}
	response := map[string]any{"scope_type": snapshot.ScopeType, "scope_id": snapshot.ScopeID, "kind": snapshot.Kind, "data": data, "supported": snapshot.Supported != 0, "reason": snapshot.Reason, "codex_version": snapshot.CodexVersion, "status": snapshot.Status, "error": snapshot.Error, "requested_at": snapshot.RequestedAt, "updated_at": snapshot.UpdatedAt}
	if thread && kind == "status.snapshot" {
		response["plan"] = map[string]any{"supported": false, "reason": "This Codex version does not expose a writable collaboration mode"}
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) codexScopeExists(r *http.Request, scopeType, scopeID string) bool {
	if scopeType == "thread" {
		_, err := a.store.Thread(r.Context(), scopeID)
		return err == nil
	}
	_, err := a.store.Workspace(r.Context(), scopeID)
	return err == nil
}

func (a *API) refreshThreadCodexStatus(w http.ResponseWriter, r *http.Request) {
	a.queueThreadCodex(w, r, "codex.status.snapshot", nil)
}
func (a *API) refreshThreadGoal(w http.ResponseWriter, r *http.Request) {
	a.queueThreadCodex(w, r, "codex.goal.get", nil)
}
func (a *API) refreshWorkspaceCodexMCP(w http.ResponseWriter, r *http.Request) {
	a.queueWorkspaceCodex(w, r, "codex.mcp.list")
}
func (a *API) refreshWorkspaceCodexSkills(w http.ResponseWriter, r *http.Request) {
	a.queueWorkspaceCodex(w, r, "codex.skills.list")
}

func (a *API) setThreadGoal(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Objective   *string `json:"objective"`
		Status      *string `json:"status"`
		TokenBudget *int64  `json:"token_budget"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Objective == nil {
		writeError(w, http.StatusBadRequest, "objective is required")
		return
	}
	objective := strings.TrimSpace(*input.Objective)
	if objective == "" || len([]rune(objective)) > 4000 {
		writeError(w, http.StatusBadRequest, "objective must contain 1-4000 characters")
		return
	}
	input.Objective = &objective
	if input.TokenBudget != nil && *input.TokenBudget <= 0 {
		writeError(w, http.StatusBadRequest, "token_budget must be positive")
		return
	}
	if input.Status != nil {
		valid := map[string]bool{"active": true, "paused": true, "blocked": true, "usageLimited": true, "budgetLimited": true, "complete": true}
		if !valid[*input.Status] {
			writeError(w, http.StatusBadRequest, "invalid goal status")
			return
		}
	}
	a.queueThreadCodex(w, r, "codex.goal.set", input)
}

func (a *API) clearThreadGoal(w http.ResponseWriter, r *http.Request) {
	a.queueThreadCodex(w, r, "codex.goal.clear", nil)
}

func (a *API) queueThreadCodex(w http.ResponseWriter, r *http.Request, kind string, extra any) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "thread not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load thread")
		return
	}
	server, err := a.store.Server(r.Context(), thread.ServerID)
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, 409, "server is offline")
		return
	}
	base := protocol.CodexSnapshotCommand{ScopeType: "thread", ScopeID: thread.ID, ThreadID: thread.ID, CodexThread: thread.CodexThreadID, Workspace: thread.Path, CodexVersion: server.CodexVersion}
	var command any = base
	if kind == "codex.goal.set" {
		raw, _ := json.Marshal(extra)
		var fields struct {
			Objective   *string `json:"objective"`
			Status      *string `json:"status"`
			TokenBudget *int64  `json:"token_budget"`
		}
		_ = json.Unmarshal(raw, &fields)
		command = protocol.CodexGoalSetCommand{CodexSnapshotCommand: base, Objective: fields.Objective, Status: fields.Status, TokenBudget: fields.TokenBudget}
	}
	snapshotKind := strings.TrimPrefix(kind, "codex.")
	if strings.HasPrefix(snapshotKind, "goal.") {
		snapshotKind = "goal"
	}
	a.queueCodex(w, r, thread.ServerID, kind, command, "thread", thread.ID, snapshotKind)
}

func (a *API) queueWorkspaceCodex(w http.ResponseWriter, r *http.Request, kind string) {
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
	command := protocol.CodexSnapshotCommand{ScopeType: "workspace", ScopeID: workspace.ID, Workspace: workspace.Path, CodexVersion: server.CodexVersion}
	a.queueCodex(w, r, workspace.ServerID, kind, command, "workspace", workspace.ID, strings.TrimPrefix(kind, "codex."))
}

func (a *API) queueCodex(w http.ResponseWriter, r *http.Request, serverID, kind string, command any, scopeType, scopeID, snapshotKind string) {
	op, err := a.store.QueueOperation(r.Context(), serverID, kind, command, kind+":"+scopeID+":"+store.NewID())
	if err != nil {
		writeError(w, 500, "could not queue Codex operation")
		return
	}
	if err := a.store.BeginCodexSnapshot(r.Context(), scopeType, scopeID, snapshotKind); err != nil {
		writeError(w, 500, "could not start Codex snapshot")
		return
	}
	a.gateway.Wake(serverID)
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": op})
}
