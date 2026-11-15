package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/geminiapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

type geminiHTTPDriver struct {
	*API
	session.Driver
}

func (d *geminiHTTPDriver) Handler(action string) http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"list":            d.geminiThreads,
		"create":          d.createGeminiThread,
		"detail":          d.geminiThread,
		"update":          d.updateGeminiThread,
		"delete":          d.deleteGeminiThread,
		"fork":            d.forkGeminiThread,
		"fork-worktree":   d.forkGeminiWorktree,
		"events":          d.geminiEvents,
		"usage":           d.geminiUsage,
		"turns":           d.startGeminiTurn,
		"steer":           d.steerGeminiTurn,
		"interrupt":       d.interruptGeminiTurn,
		"confirm-stopped": d.confirmGeminiStopped,
		"answer":          d.answerGeminiQuestion,
		"rewrite":         d.startGeminiTurn,
		"goal:get":        d.geminiGoal,
		"goal:control":    d.controlGeminiGoal,
	}[action]
}
func (a *geminiHTTPDriver) geminiThreads(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.GeminiThreads(r.Context(), r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeError(w, 500, "could not list Gemini sessions")
		return
	}
	writeJSON(w, 200, items)
}

func (a *geminiHTTPDriver) createGeminiThread(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WorkspaceID   string `json:"workspace_id"`
		ThinkingLevel string `json:"thinking_level"`
		Model         string `json:"model"`
		ApprovalMode  string `json:"approval_mode"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Model == "" {
		input.Model = "auto"
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = "default"
	}
	if !validGeminiOptions(input.Model, input.ApprovalMode) || !geminiapi.ValidThinkingLevel(input.Model, input.ThinkingLevel) {
		writeError(w, 400, "invalid Gemini model, thinking level or approval mode")
		return
	}
	if input.ThinkingLevel != "" || input.ApprovalMode == "yolo" || input.ApprovalMode == "plan" {
		workspace, err := a.store.Workspace(r.Context(), input.WorkspaceID)
		if err != nil {
			writeSessionCreateError(w, err, "workspace not found")
			return
		}
		server, err := a.store.Server(r.Context(), workspace.ServerID)
		if err != nil {
			writeStoreError(w, err, "could not load server")
			return
		}
		if input.ApprovalMode == "plan" && !buildinfo.SupportsGeminiPlan(server.AgentVersion, server.GeminiVersion) {
			writeError(w, 409, "Gemini plan mode requires Wio Agent 0.2.83 and Gemini CLI 0.66.0 or newer")
			return
		}
		if input.ApprovalMode == "yolo" && !buildinfo.SupportsGeminiYolo(server.AgentVersion) {
			writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiYoloVersion+" or newer to use Gemini without approvals")
			return
		}
		if input.ThinkingLevel != "" && !buildinfo.SupportsGeminiThinking(server.AgentVersion) {
			writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiThinkingVersion+" or newer to configure Gemini thinking")
			return
		}
	}
	thread, err := a.store.CreateGeminiThread(r.Context(), input.WorkspaceID, input.Model, input.ApprovalMode, input.ThinkingLevel)
	if err != nil {
		writeSessionCreateError(w, err, "workspace not found or unavailable")
		return
	}
	a.auditGemini(r, "gemini.thread.create", thread.ID)
	a.publishGeminiChange(r, thread.ID, protocol.EventGeminiThreadCreated)
	writeJSON(w, 201, thread)
}

func (a *geminiHTTPDriver) geminiThread(w http.ResponseWriter, r *http.Request) {
	if thread, ok := a.loadGeminiThread(w, r); ok {
		writeJSON(w, 200, thread)
	}
}

func (a *geminiHTTPDriver) updateGeminiThread(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
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
	if title == "" || utf8.RuneCountInString(title) > 180 {
		writeError(w, 400, "invalid session title")
		return
	}
	if input.Archived != nil {
		archived = *input.Archived
	}
	if err := func() error {
		if input.Title == nil && input.Archived == nil {
			return nil
		}
		return a.store.UpdateGeminiThread(r.Context(), thread.ID, title, archived)
	}(); err != nil {
		writeStoreError(w, err, "finish or interrupt the Gemini turn first")
		return
	}
	if input.Pinned != nil {
		if err := a.store.PinGeminiThread(r.Context(), thread.ID, *input.Pinned); err != nil {
			writeError(w, 500, "could not pin Gemini session")
			return
		}
	}
	a.auditGemini(r, "gemini.thread.update", thread.ID)
	a.publishGeminiChange(r, thread.ID, protocol.EventGeminiThreadUpdated)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *geminiHTTPDriver) deleteGeminiThread(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteGeminiThread(r.Context(), thread.ID); err != nil {
		writeStoreError(w, err, "finish or interrupt the Gemini turn before deleting it")
		return
	}
	a.auditGemini(r, "gemini.thread.delete", thread.ID)
	a.publishGeminiChange(r, thread.ID, protocol.EventGeminiThreadDeleted)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *geminiHTTPDriver) forkGeminiThread(w http.ResponseWriter, r *http.Request) {
	a.queueGeminiFork(w, r, false)
}

func (a *geminiHTTPDriver) forkGeminiWorktree(w http.ResponseWriter, r *http.Request) {
	a.queueGeminiFork(w, r, true)
}

func (a *geminiHTTPDriver) geminiEvents(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	after, before := int64(0), int64(0)
	var err error
	if query.Has("after") && query.Has("before") {
		writeError(w, 400, "before and after cannot be used together")
		return
	}
	if query.Has("after") {
		after, err = strconv.ParseInt(query.Get("after"), 10, 64)
	}
	if query.Has("before") {
		before, err = strconv.ParseInt(query.Get("before"), 10, 64)
	}
	if err != nil || after < 0 || before < 0 {
		writeError(w, 400, "invalid event cursor")
		return
	}
	var events []protocol.StreamEvent
	if query.Has("before") {
		events, err = a.store.EventsBefore(r.Context(), thread.ID, before, 500)
	} else if query.Has("after") {
		events, err = a.store.Events(r.Context(), thread.ID, after, 500)
	} else {
		events, err = a.store.RecentEvents(r.Context(), thread.ID, 500)
	}
	if err != nil {
		writeError(w, 500, "could not load Gemini events")
		return
	}
	writeJSON(w, 200, events)
}

func (a *geminiHTTPDriver) geminiUsage(w http.ResponseWriter, r *http.Request) {
	a.sessionUsage(w, r, session.Gemini, "Gemini ")
}

func (a *geminiHTTPDriver) startGeminiTurn(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	var input struct {
		Images []protocol.TurnImage `json:"images"`
		Prompt string               `json:"prompt"`
		Goal   *struct {
			MaxContinuations int `json:"max_continuations"`
			MaxSeconds       int `json:"max_seconds"`
		} `json:"goal"`
		ThinkingLevel *string `json:"thinking_level"`
		Model         string  `json:"model"`
		ApprovalMode  string  `json:"approval_mode"`
	}
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	level := thread.ThinkingLevel
	if input.ThinkingLevel != nil {
		level = *input.ThinkingLevel
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Model == "" {
		input.Model = thread.Model
	}
	if input.ApprovalMode == "" {
		input.ApprovalMode = thread.ApprovalMode
	}
	if !geminiapi.ValidThinkingLevel(input.Model, level) {
		writeError(w, 400, "invalid Gemini thinking level for this model")
		return
	}
	if (input.Prompt == "" && len(input.Images) == 0) || !validTurnImages(input.Images) || len(input.Prompt) > 256*1024 || !validGeminiOptions(input.Model, input.ApprovalMode) {
		writeError(w, 400, "invalid prompt, images, model or approval mode")
		return
	}
	if thread.ServerStatus != "online" {
		writeError(w, 409, "Gemini requires an online Agent; check the server connection")
		return
	}
	server, err := a.store.Server(r.Context(), thread.ServerID)
	if err != nil {
		writeError(w, 500, "could not load Gemini Agent")
		return
	}
	if !buildinfo.SupportsGemini(server.AgentVersion) {
		writeError(w, 409, "update the Agent to version "+buildinfo.MinimumGeminiVersion+" or newer to use Gemini")
		return
	}
	if input.ApprovalMode == "yolo" && !buildinfo.SupportsGeminiYolo(server.AgentVersion) {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiYoloVersion+" or newer to use Gemini without approvals")
		return
	}
	if input.ApprovalMode == "plan" && !buildinfo.SupportsGeminiPlan(server.AgentVersion, server.GeminiVersion) {
		writeError(w, 409, "Gemini plan mode requires Wio Agent 0.2.83 and Gemini CLI 0.66.0 or newer")
		return
	}
	if level != "" && !buildinfo.SupportsGeminiThinking(server.AgentVersion) {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiThinkingVersion+" or newer to configure Gemini thinking")
		return
	}
	if len(input.Images) > 0 && !buildinfo.SupportsGeminiInput(server.AgentVersion) {
		writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiInputVersion+" or newer to send Gemini images")
		return
	}
	editEventID := chi.URLParam(r, "eventID")
	if input.Goal != nil {
		if editEventID != "" || input.Prompt == "" || !store.ValidGeminiGoalLimits(input.Goal.MaxContinuations, input.Goal.MaxSeconds) {
			writeError(w, 400, "invalid Gemini goal objective or limits")
			return
		}
		if !buildinfo.SupportsGeminiGoal(server.AgentVersion, server.GeminiVersion) {
			writeError(w, 409, "Gemini goals require Wio Agent "+buildinfo.MinimumGeminiGoalVersion+" and Gemini CLI 0.66.0 or newer")
			return
		}
	}
	if editEventID != "" && !buildinfo.SupportsGeminiRewrite(server.AgentVersion) {
		writeError(w, 409, "update the Agent to version "+buildinfo.MinimumGeminiRewriteVersion+" or newer to edit Gemini history")
		return
	}
	var event protocol.StreamEvent
	if input.Goal != nil {
		goal := &protocol.GeminiGoalCommand{Objective: input.Prompt, MaxContinuations: input.Goal.MaxContinuations, MaxSeconds: input.Goal.MaxSeconds}
		event, err = a.store.QueueGeminiGoal(r.Context(), thread, input.Prompt, input.Model, input.ApprovalMode, level, input.Images, goal)
	} else {
		event, err = a.store.QueueGeminiInput(r.Context(), thread, editEventID, input.Prompt, input.Model, input.ApprovalMode, level, input.Images)
	}
	if errors.Is(err, store.ErrInvalidEditTarget) {
		writeError(w, 409, "this Gemini message has no saved native turn to rewind; original conversation was kept")
		return
	}
	if errors.Is(err, store.ErrThreadActive) {
		writeError(w, 409, "active or archived Gemini session cannot accept a turn")
		return
	}
	if err != nil {
		writeStoreError(w, err, "could not queue Gemini turn; check active workspace operations")
		return
	}
	a.userMessaged(r, "gemini", thread.ID)
	if editEventID != "" {
		a.publishGeminiChange(r, thread.ID, protocol.EventGeminiThreadUpdated)
		a.auditGemini(r, "gemini.turn.rewrite", thread.ID)
	} else {
		a.hub.Publish(event)
		a.auditGemini(r, "gemini.turn.start", thread.ID)
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (a *geminiHTTPDriver) steerGeminiTurn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Prompt      string               `json:"prompt"`
		OperationID string               `json:"operation_id"`
		RequestID   string               `json:"request_id"`
		Images      []protocol.TurnImage `json:"images"`
	}
	if !decodeJSONLimit(w, r, &input, 6<<20) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	_, requestErr := uuid.Parse(input.RequestID)
	if (input.Prompt == "" && len(input.Images) == 0) || len(input.Prompt) > 8192 || input.OperationID == "" || requestErr != nil || protocol.ValidateGeminiImages(input.Images) != nil {
		writeError(w, 400, "invalid Gemini steering instruction")
		return
	}
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if !thread.SteerSupported {
		writeError(w, 409, "Gemini steering requires Agent 0.2.79 and Gemini CLI 0.66.0 or newer")
		return
	}
	if len(input.Images) > 0 && !thread.SteerImagesSupported {
		writeError(w, 409, "running image updates require Agent 0.2.88 and Gemini CLI 0.66.0 or newer")
		return
	}
	event, id, err := a.store.QueueGeminiSteer(r.Context(), thread.ID, input.OperationID, input.RequestID, input.Prompt, input.Images...)
	if err != nil {
		writeStoreError(w, err, "could not queue request")
		return
	}
	a.userMessaged(r, "gemini", thread.ID)
	if event.EventID != "" {
		a.hub.Publish(event)
		a.auditGemini(r, "gemini.turn.steer", thread.ID)
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}

func (a *geminiHTTPDriver) interruptGeminiTurn(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if err := a.store.InterruptGeminiTurn(r.Context(), thread); err != nil {
		writeStoreError(w, err, "Gemini turn is no longer running")
		return
	}
	a.gateway.Wake(thread.ServerID)
	a.auditGemini(r, "gemini.turn.interrupt", thread.ID)
	a.publishGeminiChange(r, thread.ID, protocol.EventGeminiThreadUpdated)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (a *geminiHTTPDriver) confirmGeminiStopped(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
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
	event, err := a.store.ConfirmGeminiStopped(r.Context(), thread.ID, input.OperationID)
	if err != nil {
		writeStoreError(w, err, "Gemini task changed or no longer needs confirmation")
		return
	}
	a.auditGemini(r, "gemini.turn.confirm_stopped", thread.ID)
	a.hub.Publish(event)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *geminiHTTPDriver) answerGeminiQuestion(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	var input struct {
		Answers map[string]protocol.UserInputAnswer `json:"answers"`
	}
	if !decodeJSONLimit(w, r, &input, 128<<10) {
		return
	}
	id, err := a.store.QueueGeminiAnswer(r.Context(), thread.ID, chi.URLParam(r, "requestID"), input.Answers)
	if err != nil {
		writeQuestionAnswerError(w, err, "could not queue Gemini answer")
		return
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}

func (a *geminiHTTPDriver) queueGeminiFork(w http.ResponseWriter, r *http.Request, worktree bool) {
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

func validGeminiOptions(model, mode string) bool {
	if !credentialModelPattern.MatchString(model) || (mode != "default" && mode != "auto_edit" && mode != "yolo" && mode != "plan") {
		return false
	}
	return true
}

func (a *API) loadGeminiThread(w http.ResponseWriter, r *http.Request) (store.GeminiThread, bool) {
	thread, err := a.store.GeminiThread(r.Context(), chi.URLParam(r, "threadID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Gemini session not found")
		return thread, false
	}
	if err != nil {
		writeError(w, 500, "could not load Gemini session")
		return thread, false
	}
	return thread, true
}

func (a *API) auditGemini(r *http.Request, action, id string) {
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, action, "gemini_thread", id, map[string]string{}, clientIP(r))
}

func (a *API) publishGeminiChange(r *http.Request, id, kind string) {
	event, err := a.store.AddEvent(r.Context(), protocol.StreamEvent{StreamID: id, Kind: kind, Payload: eventPayload(map[string]string{})})
	if err == nil {
		a.hub.Publish(event)
	}
}
