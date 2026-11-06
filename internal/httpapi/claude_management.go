package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

func (a *API) archiveClaudeProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), id); err != nil {
		writeError(w, 404, "project not found")
		return
	}
	server, ok := a.projectServerScope(w, r, id)
	if !ok {
		return
	}
	n, err := a.store.ArchiveClaudeProject(r.Context(), id, server)
	if err != nil {
		writeError(w, 500, "could not archive Claude sessions")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "claude.project.archive", "project", id, map[string]any{"archived": n, "server_id": server}, clientIP(r))
	a.publishClaudeChange(r, id, "claude.thread.updated")
	writeJSON(w, 200, map[string]int64{"archived": n})
}
func (a *API) forkClaudeThread(w http.ResponseWriter, r *http.Request) {
	a.queueClaudeFork(w, r, false)
}
func (a *API) forkClaudeWorktree(w http.ResponseWriter, r *http.Request) {
	a.queueClaudeFork(w, r, true)
}
func (a *API) queueClaudeFork(w http.ResponseWriter, r *http.Request, worktree bool) {
	t, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	if t.ServerStatus != "online" || t.SessionID == "" || t.ArchivedAt != nil || !buildinfo.SupportsClaudeInput(t.AgentVersion) {
		writeError(w, 409, "Claude fork requires an initialized online session and Agent "+buildinfo.MinimumClaudeInputVersion+" or newer")
		return
	}
	var input struct {
		createWorktreeInput
		EventID string `json:"event_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var command *protocol.GitWorktreeCreateCommand
	if worktree {
		workspace, err := a.store.Workspace(r.Context(), t.WorkspaceID)
		if err != nil {
			writeError(w, 404, "workspace not found")
			return
		}
		c, err := newWorktreeCommand(workspace, input.createWorktreeInput)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		command = &c
	}
	op, target, err := a.store.QueueClaudeFork(r.Context(), t, command, input.EventID)
	if err != nil {
		writeError(w, 409, "finish the current turn and other workspace operations before continuing")
		return
	}
	a.gateway.Wake(t.ServerID)
	a.auditClaude(r, "claude.thread.fork", t.ID)
	a.publishClaudeChange(r, t.ID, "claude.thread.updated")
	writeJSON(w, 202, map[string]string{"operation_id": op, "target_thread_id": target})
}
