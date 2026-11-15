package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

type sessionForkSource struct {
	id, workspaceID, serverID, kind, eventKind, failure string
	includeWorkspace                                    bool
	queue                                               func(context.Context, string, *protocol.GitWorktreeCreateCommand) (string, string, error)
}

func (a *API) publishSessionChange(r *http.Request, id, kind string) {
	if event, err := a.store.AddEvent(r.Context(), protocol.StreamEvent{StreamID: id, Kind: kind, Payload: eventPayload(map[string]string{})}); err == nil {
		a.hub.Publish(event)
	}
}
func (a *API) archiveGeminiProject(w http.ResponseWriter, r *http.Request) {
	a.archiveEngineProject(w, r, "gemini", "Gemini", protocol.EventGeminiThreadUpdated, a.store.ArchiveGeminiProject)
}
func (a *API) archiveClaudeProject(w http.ResponseWriter, r *http.Request) {
	a.archiveEngineProject(w, r, "claude", "Claude", protocol.EventClaudeThreadUpdated, func(ctx context.Context, id string, server ...string) (int64, error) {
		return a.store.ArchiveClaudeProject(ctx, id, server[0])
	})
}
func (a *API) forkGeminiThread(w http.ResponseWriter, r *http.Request) {
	a.queueGeminiFork(w, r, false)
}
func (a *API) forkGeminiWorktree(w http.ResponseWriter, r *http.Request) {
	a.queueGeminiFork(w, r, true)
}
func (a *API) forkClaudeThread(w http.ResponseWriter, r *http.Request) {
	a.queueClaudeFork(w, r, false)
}
func (a *API) forkClaudeWorktree(w http.ResponseWriter, r *http.Request) {
	a.queueClaudeFork(w, r, true)
}

func (a *API) queueGeminiFork(w http.ResponseWriter, r *http.Request, worktree bool) {
	t, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if t.ServerStatus != "online" || t.SessionID == "" || t.ArchivedAt != nil {
		writeError(w, 409, "Gemini session must be initialized, online and unarchived")
		return
	}
	if !buildinfo.SupportsGeminiFork(t.AgentVersion) {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiForkVersion+" or newer")
		return
	}
	a.queueEngineFork(w, r, worktree, sessionForkSource{
		id: t.ID, workspaceID: t.WorkspaceID, serverID: t.ServerID, kind: "gemini", eventKind: protocol.EventGeminiThreadUpdated, includeWorkspace: true,
		failure: "could not branch Gemini conversation; finish the current turn and select a completed final reply",
		queue: func(ctx context.Context, event string, c *protocol.GitWorktreeCreateCommand) (string, string, error) {
			return a.store.QueueGeminiFork(ctx, t, event, c)
		},
	})
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
	a.queueEngineFork(w, r, worktree, sessionForkSource{
		id: t.ID, workspaceID: t.WorkspaceID, serverID: t.ServerID, kind: "claude", eventKind: protocol.EventClaudeThreadUpdated,
		failure: "finish the current turn and other workspace operations before continuing",
		queue: func(ctx context.Context, event string, c *protocol.GitWorktreeCreateCommand) (string, string, error) {
			return a.store.QueueClaudeFork(ctx, t, c, event)
		},
	})
}

func (a *API) archiveEngineProject(w http.ResponseWriter, r *http.Request, kind, name, eventKind string, archive func(context.Context, string, ...string) (int64, error)) {
	id := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), id); err != nil {
		writeStoreError(w, err, "project not found")
		return
	}
	serverID, ok := a.projectServerScope(w, r, id)
	if !ok {
		return
	}
	n, err := archive(r.Context(), id, serverID)
	if err != nil {
		writeError(w, 500, "could not archive "+name+" sessions")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, kind+".project.archive", "project", id, map[string]any{"archived": n, "server_id": serverID}, clientIP(r))
	a.publishSessionChange(r, id, eventKind)
	writeJSON(w, 200, map[string]int64{"archived": n})
}

func (a *API) queueEngineFork(w http.ResponseWriter, r *http.Request, worktree bool, source sessionForkSource) {
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
		workspace, err := a.store.Workspace(r.Context(), source.workspaceID)
		if err != nil {
			writeStoreError(w, err, "workspace not found")
			return
		}
		c, err := newWorktreeCommand(workspace, createWorktreeInput{Branch: input.Branch, Path: input.Path, BaseRef: input.BaseRef})
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		command = &c
	}
	op, target, err := source.queue(r.Context(), input.EventID, command)
	if err != nil {
		writeStoreError(w, err, source.failure)
		return
	}
	a.gateway.Wake(source.serverID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, source.kind+".thread.fork", source.kind+"_thread", source.id, map[string]string{}, clientIP(r))
	a.publishSessionChange(r, source.id, source.eventKind)
	result := map[string]string{"operation_id": op, "target_thread_id": target}
	if command != nil && source.includeWorkspace {
		result["workspace_id"] = command.TargetWorkspaceID
	}
	writeJSON(w, 202, result)
}
