package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

type threadListPageResponse struct {
	Items   []store.Thread `json:"items"`
	HasMore bool           `json:"has_more"`
	Next    *int           `json:"next"`
}

func (a *API) forkThreadToWorktree(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	if thread.ArchivedAt != nil || thread.CodexThreadID == "" || thread.Status == "queued" || thread.Status == "running" {
		writeError(w, http.StatusConflict, "Codex session cannot be continued to a worktree")
		return
	}
	workspace, err := a.store.Workspace(r.Context(), thread.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load workspace")
		return
	}
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	var input createWorktreeInput
	if !decodeJSON(w, r, &input) {
		return
	}
	command, err := newWorktreeCommand(workspace, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	command.SourceThreadID = thread.ID
	command.TargetThreadID = store.NewID()
	command.CodexThread = thread.CodexThreadID
	command.Title = thread.Title + " (continued)"
	operationID, err := a.store.QueueResourceOperation(r.Context(), workspace.ServerID, protocol.OpGitWorktreeCreate, command, "git-worktree-fork:"+command.TargetWorkspaceID, store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, true)
	if err != nil {
		writeStoreError(w, err, "could not queue worktree continuation")
		return
	}
	a.gateway.Wake(workspace.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.fork_worktree", "thread", thread.ID, map[string]string{"operation_id": operationID, "target_workspace_id": command.TargetWorkspaceID, "target_thread_id": command.TargetThreadID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "workspace_id": command.TargetWorkspaceID, "target_thread_id": command.TargetThreadID})
}

func (a *API) threads(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	archived := query.Get("archived")
	if archived != "" && archived != "true" && archived != "all" {
		writeError(w, http.StatusBadRequest, "archived must be true or all")
		return
	}
	limitValues, hasLimit := query["limit"]
	offsetValues, hasOffset := query["offset"]
	if hasLimit || hasOffset {
		limit, err := parseThreadPageValue(limitValues, hasLimit, "limit", defaultThreadsPageLimit, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if limit > maxThreadsPageLimit {
			limit = maxThreadsPageLimit
		}
		offset, err := parseThreadPageValue(offsetValues, hasOffset, "offset", 0, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := a.store.ListThreadsPage(r.Context(), archived, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list Codex sessions")
			return
		}
		var next *int
		if page.HasMore {
			value := offset + len(page.Items)
			next = &value
		}
		writeJSON(w, http.StatusOK, threadListPageResponse{Items: page.Items, HasMore: page.HasMore, Next: next})
		return
	}
	threads, err := a.store.ListThreads(r.Context(), archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list Codex sessions")
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

func parseThreadPageValue(values []string, present bool, name string, fallback int, positive bool) (int, error) {
	if !present {
		return fallback, nil
	}
	kind := "non-negative"
	if positive {
		kind = "positive"
	}
	if len(values) != 1 || values[0] == "" {
		return 0, fmt.Errorf("%s must be a %s decimal integer", name, kind)
	}
	for _, character := range values[0] {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%s must be a %s decimal integer", name, kind)
		}
	}
	parsed, err := strconv.ParseInt(values[0], 10, 0)
	if err != nil || (positive && parsed == 0) {
		return 0, fmt.Errorf("%s must be a %s decimal integer", name, kind)
	}
	return int(parsed), nil
}

func (a *API) thread(w http.ResponseWriter, r *http.Request) {
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

func (a *API) updateThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title    *string `json:"title"`
		Pinned   *bool   `json:"pinned"`
		Archived *bool   `json:"archived"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Title == nil && input.Pinned == nil && input.Archived == nil {
		writeError(w, http.StatusBadRequest, "at least one thread field is required")
		return
	}
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "thread title is required")
			return
		}
		if utf8.RuneCountInString(title) > threadTitleLimit {
			writeError(w, http.StatusBadRequest, "thread title is too long")
			return
		}
		input.Title = &title
	}
	threadID := chi.URLParam(r, "threadID")
	if input.Archived != nil && *input.Archived {
		current, err := a.store.Thread(r.Context(), threadID)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Codex session not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load Codex session")
			return
		}
		if current.Status == "queued" || current.Status == "running" {
			writeError(w, http.StatusConflict, "active Codex session cannot be archived")
			return
		}
	}
	thread, err := a.store.UpdateThread(r.Context(), threadID, input.Title, input.Pinned, input.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update Codex session")
		return
	}
	detail := map[string]any{}
	if input.Title != nil {
		detail["title"] = *input.Title
	}
	if input.Pinned != nil {
		detail["pinned"] = *input.Pinned
	}
	if input.Archived != nil {
		detail["archived"] = *input.Archived
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.update", "thread", thread.ID, detail, clientIP(r))
	writeJSON(w, http.StatusOK, thread)
}

func (a *API) archiveProjectThreads(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), projectID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	serverID, ok := a.projectServerScope(w, r, projectID)
	if !ok {
		return
	}
	count, err := a.store.ArchiveProjectThreads(r.Context(), projectID, serverID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not archive project sessions")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.archive_project", "project", projectID, map[string]any{"archived": count, "server_id": serverID}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]int64{"archived": count})
}

func (a *API) forkThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		EventID string `json:"event_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	thread, err := a.store.Thread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	if thread.ArchivedAt != nil {
		writeError(w, http.StatusConflict, "archived Codex session is read-only")
		return
	}
	if thread.CodexThreadID == "" {
		writeError(w, http.StatusConflict, "Codex session must be initialized before it can be continued")
		return
	}
	if thread.Status == "queued" || thread.Status == "running" {
		writeError(w, http.StatusConflict, "active Codex session cannot be continued yet")
		return
	}
	server, err := a.store.Server(r.Context(), thread.ServerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	targetID := store.NewID()
	command := protocol.ForkThreadCommand{SourceThreadID: thread.ID, TargetThreadID: targetID, CodexThread: thread.CodexThreadID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Title: thread.Title + " (continued)"}
	if input.EventID != "" {
		if !buildinfo.SupportsMessageFork(server.AgentVersion) {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "fork_agent_update", "error": "Branching from a message requires Agent 0.2.59 or newer"})
			return
		}
		command.LastTurnID, command.CutoffSequence, err = a.store.ThreadForkBoundary(r.Context(), thread.ID, input.EventID)
		if errors.Is(err, store.ErrInvalidForkTarget) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"code": "fork_invalid_target", "error": err.Error()})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not resolve fork boundary")
			return
		}
		command.Title = thread.Title + " (branch)"
	}
	opID, err := a.store.QueueResourceOperation(r.Context(), thread.ServerID, protocol.OpCodexThreadFork, command, "codex-fork:"+targetID, store.OperationResource{ThreadID: thread.ID}, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue Codex session continuation")
		return
	}
	a.gateway.Wake(thread.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.fork", "thread", thread.ID, map[string]string{"operation_id": opID, "target_thread_id": targetID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "target_thread_id": targetID})
}

func (a *API) createThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		// Kept for compatibility with older cached web clients. Codex now names threads.
		Title string `json:"title"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	thread, err := a.store.CreateThread(r.Context(), input.WorkspaceID, "")
	if err != nil {
		writeSessionCreateError(w, err, "could not create Codex session")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.create", "thread", thread.ID, map[string]string{"workspace_id": input.WorkspaceID}, clientIP(r))
	writeJSON(w, http.StatusCreated, thread)
}

func (a *API) deleteThread(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "threadID")
	thread, err := a.store.Thread(r.Context(), threadID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load Codex session")
		return
	}
	if err := a.store.DeleteThread(r.Context(), threadID); errors.Is(err, store.ErrThreadActive) {
		writeError(w, http.StatusConflict, "active Codex session must be interrupted before deletion")
		return
	} else if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete Codex session")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.thread.delete", "thread", thread.ID, map[string]string{"title": thread.Title, "workspace_id": thread.WorkspaceID}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) threadEvents(w http.ResponseWriter, r *http.Request) {
	const (
		defaultThreadEventsLimit = 500
		maxThreadEventsLimit     = 1000
	)
	query := r.URL.Query()
	afterValue, hasAfter := query["after"]
	beforeValue, hasBefore := query["before"]
	if hasAfter && hasBefore {
		writeError(w, http.StatusBadRequest, "before and after cannot be used together")
		return
	}
	var after int64
	if hasAfter && len(afterValue) > 0 && afterValue[0] != "" {
		parsedAfter, err := strconv.ParseInt(afterValue[0], 10, 64)
		if err != nil || parsedAfter < 0 {
			writeError(w, http.StatusBadRequest, "after must be a non-negative sequence number")
			return
		}
		after = parsedAfter
	} else if hasAfter {
		writeError(w, http.StatusBadRequest, "after must be a non-negative sequence number")
		return
	}
	var before int64
	if hasBefore && len(beforeValue) > 0 && beforeValue[0] != "" {
		parsedBefore, err := strconv.ParseInt(beforeValue[0], 10, 64)
		if err != nil || parsedBefore < 0 {
			writeError(w, http.StatusBadRequest, "before must be a non-negative sequence number")
			return
		}
		before = parsedBefore
	} else if hasBefore {
		writeError(w, http.StatusBadRequest, "before must be a non-negative sequence number")
		return
	}

	limit := defaultThreadEventsLimit
	if limitValue := query.Get("limit"); limitValue != "" {
		if parsedLimit, err := strconv.Atoi(limitValue); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		}
	}
	if limit > maxThreadEventsLimit {
		limit = maxThreadEventsLimit
	}

	threadID := chi.URLParam(r, "threadID")
	var events []protocol.StreamEvent
	var err error
	if query.Get("view") == "raw" {
		if hasAfter {
			events, err = a.store.Events(r.Context(), threadID, after, limit)
		} else if hasBefore {
			events, err = a.store.EventsBefore(r.Context(), threadID, before, limit)
		} else {
			events, err = a.store.RecentEvents(r.Context(), threadID, limit)
		}
	} else {
		if hasAfter {
			events, err = a.store.ConversationEvents(r.Context(), threadID, after, limit)
		} else if hasBefore {
			events, err = a.store.ConversationEventsBefore(r.Context(), threadID, before, limit)
		} else {
			events, err = a.store.RecentConversationEvents(r.Context(), threadID, limit)
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session events")
		return
	}
	writeJSON(w, http.StatusOK, events)
}
