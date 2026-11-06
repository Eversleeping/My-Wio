package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func validClaudeOptions(model, mode string) bool {
	// Claude Code also accepts model aliases with the extended-context suffix.
	baseModel := strings.TrimSuffix(model, "[1m]")
	return len(model) <= 128 && credentialModelPattern.MatchString(baseModel) && (mode == "default" || mode == "acceptEdits" || mode == "plan" || mode == "bypassPermissions")
}

func (a *API) claudeThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := a.store.ClaudeThreads(r.Context(), r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeError(w, 500, "could not list Claude sessions")
		return
	}
	writeJSON(w, 200, threads)
}

func (a *API) createClaudeThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID    string `json:"workspace_id"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
		Effort         string `json:"effort"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Model == "" {
		workspace, err := a.store.Workspace(r.Context(), input.WorkspaceID)
		if err != nil {
			writeError(w, 400, "workspace not found or unavailable")
			return
		}
		server, err := a.store.Server(r.Context(), workspace.ServerID)
		if err != nil {
			writeError(w, 400, "server not found or unavailable")
			return
		}
		input.Model = server.ClaudeProfileModel
		if input.Model == "" {
			input.Model = "sonnet"
		}
	}
	if input.PermissionMode == "" {
		input.PermissionMode = "default"
	}
	if !validClaudeOptions(input.Model, input.PermissionMode) || !protocol.ValidClaudeEffort(input.Effort) {
		writeError(w, 400, "invalid Claude model, permission mode or effort")
		return
	}
	thread, err := a.store.CreateClaudeThread(r.Context(), input.WorkspaceID, input.Model, input.PermissionMode, input.Effort)
	if err != nil {
		writeError(w, 400, "workspace not found or unavailable")
		return
	}
	a.auditClaude(r, "claude.thread.create", thread.ID)
	a.publishClaudeChange(r, thread.ID, "claude.thread.created")
	writeJSON(w, 201, thread)
}

func (a *API) loadClaudeThread(w http.ResponseWriter, r *http.Request) (store.ClaudeThread, bool) {
	thread, err := a.store.ClaudeThread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Claude session not found")
		return thread, false
	}
	if err != nil {
		writeError(w, 500, "could not load Claude session")
		return thread, false
	}
	return thread, true
}

func (a *API) claudeThread(w http.ResponseWriter, r *http.Request) {
	if thread, ok := a.loadClaudeThread(w, r); ok {
		writeJSON(w, 200, thread)
	}
}

func (a *API) claudeEvents(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	var cursor int64
	var err error
	if query.Has("after") && query.Has("before") {
		writeError(w, 400, "before and after cannot be used together")
		return
	}
	if query.Has("before") {
		cursor, err = strconv.ParseInt(query.Get("before"), 10, 64)
	} else if query.Has("after") {
		cursor, err = strconv.ParseInt(query.Get("after"), 10, 64)
	}
	if err != nil || cursor < 0 {
		writeError(w, 400, "invalid event cursor")
		return
	}
	var events []protocol.StreamEvent
	if query.Has("before") {
		events, err = a.store.EventsBefore(r.Context(), thread.ID, cursor, 500)
	} else if query.Has("after") {
		events, err = a.store.Events(r.Context(), thread.ID, cursor, 500)
	} else {
		events, err = a.store.RecentEvents(r.Context(), thread.ID, 500)
	}
	if err != nil {
		writeError(w, 500, "could not load Claude events")
		return
	}
	writeJSON(w, 200, events)
}

func (a *API) startClaudeTurn(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	var input struct {
		Prompt         string               `json:"prompt"`
		Images         []protocol.TurnImage `json:"images"`
		Model          string               `json:"model"`
		PermissionMode string               `json:"permission_mode"`
		Effort         *string              `json:"effort"`
	}
	if !decodeJSONLimit(w, r, &input, 7<<20) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	effort := thread.Effort
	if input.Effort != nil {
		effort = *input.Effort
	}
	if input.Model == "" {
		input.Model = thread.Model
	}
	if input.PermissionMode == "" {
		input.PermissionMode = thread.PermissionMode
	}
	if (input.Prompt == "" && len(input.Images) == 0) || !validTurnImages(input.Images) || len(input.Prompt) > 256*1024 || !validClaudeOptions(input.Model, input.PermissionMode) || !protocol.ValidClaudeEffort(effort) {
		writeError(w, 400, "invalid prompt, Claude model, permission mode or effort")
		return
	}
	if thread.ServerStatus != "online" {
		writeError(w, 409, "Claude requires an online Agent")
		return
	}
	if !buildinfo.SupportsClaude(thread.AgentVersion) {
		writeError(w, 409, "update the Agent to version "+buildinfo.MinimumClaudeVersion+" or newer to use Claude")
		return
	}
	if effort != "" && !thread.EffortSupported {
		writeError(w, 409, "update the Agent to version "+buildinfo.MinimumClaudeEffortVersion+" or newer to select Claude effort")
		return
	}
	if (len(input.Images) > 0 || (thread.SessionID == "" && thread.ForkSnapshotID != "")) && !thread.InputSupported {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumClaudeInputVersion+" or newer to use Claude images and session forks")
		return
	}
	editID := chi.URLParam(r, "eventID")
	if editID != "" && !thread.RewriteSupported {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumClaudeRewriteVersion+" or newer to edit Claude messages")
		return
	}
	var event protocol.StreamEvent
	var err error
	if editID != "" {
		event, err = a.store.QueueClaudeRewrite(r.Context(), thread, editID, input.Prompt, input.Model, input.PermissionMode, effort, input.Images...)
	} else {
		event, err = a.store.QueueClaudeTurn(r.Context(), thread, input.Prompt, input.Model, input.PermissionMode, effort, input.Images...)
	}
	if err != nil {
		writeError(w, 409, "could not queue Claude turn; check session and active workspace operations")
		return
	}
	if event.EventID != "" {
		a.hub.Publish(event)
	}
	if editID != "" {
		a.auditClaude(r, "claude.turn.rewrite", thread.ID)
		a.publishClaudeChange(r, thread.ID, "claude.thread.updated")
	} else {
		a.auditClaude(r, "claude.turn.start", thread.ID)
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (a *API) interruptClaudeTurn(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	if err := a.store.InterruptClaudeTurn(r.Context(), thread); err != nil {
		writeError(w, 409, "Claude turn is no longer running")
		return
	}
	a.gateway.Wake(thread.ServerID)
	a.auditClaude(r, "claude.turn.interrupt", thread.ID)
	a.publishClaudeChange(r, thread.ID, "claude.thread.updated")
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (a *API) confirmClaudeStopped(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	var input struct {
		OperationID string `json:"operation_id"`
		Confirmed   bool   `json:"confirmed"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirmed || input.OperationID == "" {
		writeError(w, 400, "confirmation and operation ID are required")
		return
	}
	if err := a.store.ConfirmClaudeStopped(r.Context(), thread.ID, input.OperationID); err != nil {
		writeError(w, 409, "Claude task changed or no longer needs confirmation")
		return
	}
	a.auditClaude(r, "claude.turn.confirm_stopped", thread.ID)
	a.publishClaudeChange(r, thread.ID, "claude.thread.updated")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) updateClaudeThread(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	var input struct {
		Title    *string `json:"title"`
		Archived *bool   `json:"archived"`
		Pinned   *bool   `json:"pinned"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	title, archived := thread.Title, thread.ArchivedAt != nil
	if input.Title != nil {
		title = strings.TrimSpace(*input.Title)
	}
	if input.Archived != nil {
		archived = *input.Archived
	}
	if title == "" || utf8.RuneCountInString(title) > 180 {
		writeError(w, 400, "invalid session title")
		return
	}
	if input.Title != nil || input.Archived != nil {
		if err := a.store.UpdateClaudeThread(r.Context(), thread.ID, title, archived); err != nil {
			writeError(w, 409, "finish or interrupt the Claude turn first")
			return
		}
	}
	if input.Pinned != nil {
		if err := a.store.PinClaudeThread(r.Context(), thread.ID, *input.Pinned); err != nil {
			writeError(w, 500, "could not pin Claude session")
			return
		}
	}
	a.auditClaude(r, "claude.thread.update", thread.ID)
	a.publishClaudeChange(r, thread.ID, "claude.thread.updated")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) deleteClaudeThread(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadClaudeThread(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteClaudeThread(r.Context(), thread.ID); err != nil {
		writeError(w, 409, "finish or interrupt the Claude turn before deleting it")
		return
	}
	a.auditClaude(r, "claude.thread.delete", thread.ID)
	a.publishClaudeChange(r, thread.ID, "claude.thread.deleted")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) auditClaude(r *http.Request, action, id string) {
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, action, "claude_thread", id, map[string]string{}, clientIP(r))
}
func (a *API) publishClaudeChange(r *http.Request, id, kind string) {
	if event, err := a.store.AddEvent(r.Context(), protocol.StreamEvent{StreamID: id, Kind: kind, Payload: eventPayload(map[string]string{})}); err == nil {
		a.hub.Publish(event)
	}
}
