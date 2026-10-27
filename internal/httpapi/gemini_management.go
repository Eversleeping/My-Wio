package httpapi

import (
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"net/http"
)

func (a *API) archiveGeminiProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), id); err != nil {
		writeError(w, 404, "project not found")
		return
	}
	serverID, ok := a.projectServerScope(w, r, id)
	if !ok {
		return
	}
	n, err := a.store.ArchiveGeminiProject(r.Context(), id, serverID)
	if err != nil {
		writeError(w, 500, "could not archive Gemini sessions")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "gemini.project.archive", "project", id, map[string]any{"archived": n, "server_id": serverID}, clientIP(r))
	a.publishGeminiChange(r, id, "gemini.thread.updated")
	writeJSON(w, 200, map[string]int64{"archived": n})
}

func (a *API) forkGeminiThread(w http.ResponseWriter, r *http.Request) {
	a.queueGeminiFork(w, r, false)
}
func (a *API) forkGeminiWorktree(w http.ResponseWriter, r *http.Request) {
	a.queueGeminiFork(w, r, true)
}
func (a *API) queueGeminiFork(w http.ResponseWriter, r *http.Request, worktree bool) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if thread.ServerStatus != "online" || thread.SessionID == "" || thread.ArchivedAt != nil {
		writeError(w, 409, "Gemini session must be initialized, online and unarchived")
		return
	}
	if !buildinfo.SupportsGeminiFork(thread.AgentVersion) {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiForkVersion+" or newer")
		return
	}
	var input struct {
		EventID string `json:"event_id"`
		Branch  string `json:"branch"`
		Path    string `json:"path"`
		BaseRef string `json:"base_ref"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var command *protocol.GitWorktreeCreateCommand
	if worktree {
		workspace, err := a.store.Workspace(r.Context(), thread.WorkspaceID)
		if err != nil {
			writeError(w, 404, "workspace not found")
			return
		}
		c, err := newWorktreeCommand(workspace, createWorktreeInput{Branch: input.Branch, Path: input.Path, BaseRef: input.BaseRef})
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		command = &c
	}
	op, target, err := a.store.QueueGeminiFork(r.Context(), thread, input.EventID, command)
	if err != nil {
		writeError(w, 409, "could not branch Gemini conversation; finish the current turn and select a completed final reply")
		return
	}
	a.gateway.Wake(thread.ServerID)
	a.auditGemini(r, "gemini.thread.fork", thread.ID)
	a.publishGeminiChange(r, thread.ID, "gemini.thread.updated")
	result := map[string]string{"operation_id": op, "target_thread_id": target}
	if command != nil {
		result["workspace_id"] = command.TargetWorkspaceID
	}
	writeJSON(w, 202, result)
}
