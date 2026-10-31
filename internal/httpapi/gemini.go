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
	"github.com/wio-platform/wio/internal/geminiapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func validGeminiOptions(model, mode string) bool {
	if !credentialModelPattern.MatchString(model) || (mode != "default" && mode != "auto_edit" && mode != "yolo" && mode != "plan") {
		return false
	}
	return true
}

func (a *API) geminiThreads(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.GeminiThreads(r.Context(), r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeError(w, 500, "could not list Gemini sessions")
		return
	}
	writeJSON(w, 200, items)
}

func (a *API) createGeminiThread(w http.ResponseWriter, r *http.Request) {
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
			writeError(w, 400, "workspace not found")
			return
		}
		server, err := a.store.Server(r.Context(), workspace.ServerID)
		if input.ApprovalMode == "plan" && (err != nil || !buildinfo.SupportsGeminiPlan(server.AgentVersion, server.GeminiVersion)) {
			writeError(w, 409, "Gemini plan mode requires Wio Agent 0.2.83 and Gemini CLI 0.66.0 or newer")
			return
		}
		if input.ApprovalMode == "yolo" && (err != nil || !buildinfo.SupportsGeminiYolo(server.AgentVersion)) {
			writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiYoloVersion+" or newer to use Gemini without approvals")
			return
		}
		if err != nil || (input.ThinkingLevel != "" && !buildinfo.SupportsGeminiThinking(server.AgentVersion)) {
			writeError(w, 409, "update Agent to "+buildinfo.MinimumGeminiThinkingVersion+" or newer to configure Gemini thinking")
			return
		}
	}
	thread, err := a.store.CreateGeminiThread(r.Context(), input.WorkspaceID, input.Model, input.ApprovalMode, input.ThinkingLevel)
	if err != nil {
		writeError(w, 400, "workspace not found or unavailable")
		return
	}
	a.auditGemini(r, "gemini.thread.create", thread.ID)
	a.publishGeminiChange(r, thread.ID, "gemini.thread.created")
	writeJSON(w, 201, thread)
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

func (a *API) geminiThread(w http.ResponseWriter, r *http.Request) {
	if thread, ok := a.loadGeminiThread(w, r); ok {
		writeJSON(w, 200, thread)
	}
}

func (a *API) geminiEvents(w http.ResponseWriter, r *http.Request) {
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

func (a *API) startGeminiTurn(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 409, "could not queue Gemini turn; check active workspace operations")
		return
	}
	if editEventID != "" {
		a.publishGeminiChange(r, thread.ID, "gemini.thread.updated")
		a.auditGemini(r, "gemini.turn.rewrite", thread.ID)
	} else {
		a.hub.Publish(event)
		a.auditGemini(r, "gemini.turn.start", thread.ID)
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (a *API) interruptGeminiTurn(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if err := a.store.InterruptGeminiTurn(r.Context(), thread); err != nil {
		writeError(w, 409, "Gemini turn is no longer running")
		return
	}
	a.gateway.Wake(thread.ServerID)
	a.auditGemini(r, "gemini.turn.interrupt", thread.ID)
	a.publishGeminiChange(r, thread.ID, "gemini.thread.updated")
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (a *API) confirmGeminiStopped(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 409, "Gemini task changed or no longer needs confirmation")
		return
	}
	a.auditGemini(r, "gemini.turn.confirm_stopped", thread.ID)
	a.hub.Publish(event)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) answerGeminiQuestion(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 409, err.Error())
		return
	}
	a.gateway.Wake(thread.ServerID)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}

func (a *API) updateGeminiThread(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 409, "finish or interrupt the Gemini turn first")
		return
	}
	if input.Pinned != nil {
		if err := a.store.PinGeminiThread(r.Context(), thread.ID, *input.Pinned); err != nil {
			writeError(w, 500, "could not pin Gemini session")
			return
		}
	}
	a.auditGemini(r, "gemini.thread.update", thread.ID)
	a.publishGeminiChange(r, thread.ID, "gemini.thread.updated")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *API) deleteGeminiThread(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteGeminiThread(r.Context(), thread.ID); err != nil {
		writeError(w, 409, "finish or interrupt the Gemini turn before deleting it")
		return
	}
	a.auditGemini(r, "gemini.thread.delete", thread.ID)
	a.publishGeminiChange(r, thread.ID, "gemini.thread.deleted")
	writeJSON(w, 200, map[string]bool{"ok": true})
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
