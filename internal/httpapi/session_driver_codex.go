package httpapi

import (
	"database/sql"
	"errors"
	"github.com/wio-platform/wio/internal/buildinfo"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

type codexHTTPDriver struct {
	*API
	session.Driver
}

func (d *codexHTTPDriver) Handler(action string) http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"create":         d.createThread,
		"update":         d.updateThread,
		"delete":         d.deleteThread,
		"fork":           d.forkThread,
		"fork-worktree":  d.forkThreadToWorktree,
		"usage":          d.threadUsage,
		"turns":          d.startTurn,
		"rewrite":        d.rewriteTurn,
		"steer":          d.steerTurn,
		"interrupt":      d.interruptTurn,
		"goal:get":       d.threadGoal,
		"goal:set":       d.setThreadGoal,
		"goal:clear":     d.clearThreadGoal,
		"goal:refresh":   d.refreshThreadGoal,
		"compact":        d.compactThread,
		"status":         d.threadCodexStatus,
		"status:refresh": d.refreshThreadCodexStatus,
	}[action]
}
func (a *codexHTTPDriver) createThread(w http.ResponseWriter, r *http.Request) {
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

func (a *codexHTTPDriver) updateThread(w http.ResponseWriter, r *http.Request) {
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

func (a *codexHTTPDriver) deleteThread(w http.ResponseWriter, r *http.Request) {
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

func (a *codexHTTPDriver) forkThread(w http.ResponseWriter, r *http.Request) {
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
		if !server.Supports(protocol.CapabilityCodexForkAtMessage) {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "fork_agent_update", "error": "Branching from a message requires Agent " + buildinfo.MinimumAgentVersion + " or newer"})
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

func (a *codexHTTPDriver) forkThreadToWorktree(w http.ResponseWriter, r *http.Request) {
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

func (a *codexHTTPDriver) threadUsage(w http.ResponseWriter, r *http.Request) {
	a.sessionUsage(w, r, session.Codex, "")
}

func (a *codexHTTPDriver) startTurn(w http.ResponseWriter, r *http.Request) {
	a.handleTurn(w, r, "")
}

func (a *codexHTTPDriver) rewriteTurn(w http.ResponseWriter, r *http.Request) {
	a.handleTurn(w, r, chi.URLParam(r, "eventID"))
}

func (a *codexHTTPDriver) steerTurn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string               `json:"request_id"`
		Prompt    string               `json:"prompt"`
		Images    []protocol.TurnImage `json:"images"`
	}
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Prompt == "" && len(input.Images) == 0 {
		writeError(w, http.StatusBadRequest, "prompt or image is required")
		return
	}
	if !validTurnImages(input.Images) {
		writeError(w, http.StatusBadRequest, "invalid or oversized turn images")
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if len(input.RequestID) > 128 {
		writeError(w, http.StatusBadRequest, "request_id is too long")
		return
	}
	if a.replayCodexRequest(w, r, input.RequestID, "steer", store.CodexRequestInput{Prompt: input.Prompt, Images: input.Images}) {
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
	if thread.ArchivedAt != nil || thread.Status != "running" || thread.CodexThreadID == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_inactive", "error": "Codex turn is not running; keep your correction and send it after the session starts or finishes"})
		return
	}
	server, err := a.store.Server(r.Context(), thread.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_offline", "error": "Agent must be online to steer a running turn"})
		return
	}
	if !server.Supports(protocol.CapabilityCodexSteer) {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_agent_update", "error": "Steering requires Agent " + buildinfo.MinimumAgentVersion + " or newer; update it after the current task finishes"})
		return
	}
	turnID, err := a.store.LatestActiveTurnID(r.Context(), thread.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")
		return
	}
	if turnID == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_inactive", "error": "the target Codex turn is no longer active; send the correction as a new message"})
		return
	}
	command := protocol.SteerTurnCommand{ThreadID: thread.ID, CodexThread: thread.CodexThreadID, ExpectedTurnID: turnID, Prompt: input.Prompt, Images: input.Images}
	operationID, event, err := a.store.QueueCodexSteerRequest(r.Context(), thread, command, input.RequestID)
	if errors.Is(err, store.ErrThreadActive) {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_inactive", "error": "the target Codex turn is no longer active; send the correction as a new message"})
		return
	}
	if err != nil {
		writeStoreError(w, err, "could not queue Codex steering")
		return
	}
	a.userMessaged(r, "codex", thread.ID)
	if event.EventID != "" {
		a.hub.Publish(event)
	}
	a.gateway.Wake(thread.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "codex.turn.steer", "thread", thread.ID, map[string]string{"operation_id": operationID, "turn_id": turnID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *codexHTTPDriver) interruptTurn(w http.ResponseWriter, r *http.Request) {
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
	turnID, err := a.store.LatestActiveTurnID(r.Context(), thread.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")
		return
	}
	activeOperation, operationErr := a.store.ActiveCodexTurnOperation(r.Context(), thread.ID)
	if operationErr != nil && !errors.Is(operationErr, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "could not identify queued Codex turn")
		return
	}
	if operationErr == nil && activeOperation.Status == "queued" {
		cancelled, cancelErr := a.store.CancelOperation(r.Context(), activeOperation.ID, "cancelled before Codex turn started")
		if cancelErr != nil {
			writeError(w, http.StatusInternalServerError, "could not cancel queued Codex turn")
			return
		}
		if cancelled {
			_ = a.store.SetThreadStatus(r.Context(), thread.ID, "idle")
			_ = a.store.MarkScheduledTaskByOperation(r.Context(), activeOperation.ID, "cancelled", "cancelled before Codex turn started")
			event, eventErr := a.store.AddEvent(r.Context(), protocol.StreamEvent{StreamID: thread.ID, Kind: protocol.EventCodexTurnCancelled, Payload: eventPayload(map[string]string{"operation_id": activeOperation.ID, "source": "control", "text": "cancelled before Codex turn started"})})
			if eventErr == nil {
				a.hub.Publish(event)
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"operation_id": activeOperation.ID, "cancelled": true})
			return
		}
		// A concurrent Agent delivery won the race. Re-read the event stream;
		// if a turn was accepted we can send a precise interrupt below.
		turnID, err = a.store.LatestActiveTurnID(r.Context(), thread.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "could not identify active Codex turn")
			return
		}
	}
	if turnID == "" && operationErr != nil {
		writeError(w, http.StatusConflict, "active Codex turn is not ready to interrupt")
		return
	}
	command := protocol.InterruptTurnCommand{ThreadID: thread.ID, CodexThread: thread.CodexThreadID, TurnID: turnID}
	operationID, err := a.store.QueueResourceOperation(r.Context(), thread.ServerID, protocol.OpCodexTurnInterrupt, command, "codex-interrupt:"+store.NewID(), store.OperationResource{ThreadID: thread.ID}, false)
	if err != nil {
		writeStoreError(w, err, "could not queue interrupt")
		return
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}

func (a *codexHTTPDriver) handleTurn(w http.ResponseWriter, r *http.Request, routeEditEventID string) {
	threadID := chi.URLParam(r, "threadID")
	var input struct {
		RequestID       string               `json:"request_id"`
		Prompt          string               `json:"prompt"`
		Images          []protocol.TurnImage `json:"images"`
		Model           string               `json:"model"`
		ReasoningEffort string               `json:"reasoning_effort"`
		ApprovalMode    string               `json:"approval_mode"`
		EditEventID     string               `json:"edit_event_id"`
	}
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	if routeEditEventID != "" {
		if input.EditEventID != "" && input.EditEventID != routeEditEventID {
			writeError(w, http.StatusBadRequest, "conflicting edit target")
			return
		}
		input.EditEventID = routeEditEventID
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Model = strings.TrimSpace(input.Model)
	input.ReasoningEffort = strings.TrimSpace(input.ReasoningEffort)
	if input.Prompt == "" && len(input.Images) == 0 {
		writeError(w, http.StatusBadRequest, "prompt or image is required")
		return
	}
	if !validTurnImages(input.Images) {
		writeError(w, http.StatusBadRequest, "invalid or oversized turn images")
		return
	}
	if utf8.RuneCountInString(input.Model) > 128 {
		writeError(w, http.StatusBadRequest, "model is too long")
		return
	}
	if !validReasoningEffort(input.ReasoningEffort) {
		writeError(w, http.StatusBadRequest, "invalid reasoning_effort")
		return
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = "on-request"
	}
	if input.ApprovalMode != "on-request" && input.ApprovalMode != "untrusted" && input.ApprovalMode != "never" {
		writeError(w, http.StatusBadRequest, "invalid approval_mode")
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if len(input.RequestID) > 128 {
		writeError(w, http.StatusBadRequest, "request_id is too long")
		return
	}
	if input.EditEventID == "" && a.replayCodexRequest(w, r, input.RequestID, "turn", store.CodexRequestInput{Prompt: input.Prompt, Images: input.Images, Model: input.Model, ReasoningEffort: input.ReasoningEffort, ApprovalMode: input.ApprovalMode}) {
		return
	}
	thread, err := a.store.Thread(r.Context(), threadID)
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
	command := protocol.StartTurnCommand{ThreadID: thread.ID, CodexThread: thread.CodexThreadID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: input.Prompt, Images: input.Images, Model: input.Model, ReasoningEffort: input.ReasoningEffort, ApprovalMode: input.ApprovalMode}
	if input.EditEventID != "" {
		operationID, _, err := a.store.RewriteThread(r.Context(), thread, input.EditEventID, command, eventPayload(map[string]any{"text": input.Prompt, "image_count": len(input.Images)}))
		if errors.Is(err, store.ErrThreadActive) {
			writeError(w, http.StatusConflict, "active Codex session must finish before editing an earlier message")
			return
		}
		if errors.Is(err, store.ErrInvalidEditTarget) {
			writeError(w, http.StatusBadRequest, "message is no longer available to edit")
			return
		}
		if err != nil {
			writeStoreError(w, err, "could not rewrite Codex session")
			return
		}
		a.userMessaged(r, "codex", thread.ID)
		a.gateway.Wake(thread.ServerID)
		session := currentSession(r)
		_ = a.store.Audit(r.Context(), session.UserID, "codex.turn.rewrite", "thread", thread.ID, map[string]string{"operation_id": operationID, "edit_event_id": input.EditEventID}, clientIP(r))
		writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
		return
	}
	operationID, event, err := a.store.QueueCodexTurnRequest(r.Context(), thread, command, input.RequestID)
	if errors.Is(err, store.ErrThreadActive) {
		writeError(w, http.StatusConflict, "active Codex session must finish before starting another turn")
		return
	}
	if err != nil {
		writeStoreError(w, err, "could not queue Codex turn")
		return
	}
	a.userMessaged(r, "codex", thread.ID)
	if event.EventID != "" {
		a.hub.Publish(event)
	}
	a.gateway.Wake(thread.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.turn.start", "thread", thread.ID, map[string]string{"operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}
