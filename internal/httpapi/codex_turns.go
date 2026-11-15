package httpapi

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (a *API) startTurn(w http.ResponseWriter, r *http.Request) {
	a.handleTurn(w, r, "")
}

func (a *API) rewriteTurn(w http.ResponseWriter, r *http.Request) {
	a.handleTurn(w, r, chi.URLParam(r, "eventID"))
}

func (a *API) steerTurn(w http.ResponseWriter, r *http.Request) {
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
	if !buildinfo.SupportsCodexSteer(server.AgentVersion) {
		writeJSON(w, http.StatusConflict, map[string]string{"code": "steer_agent_update", "error": "Steering requires Agent 0.2.51 or newer; update it after the current task finishes"})
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

func (a *API) handleTurn(w http.ResponseWriter, r *http.Request, routeEditEventID string) {
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

func validReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func validTurnImages(images []protocol.TurnImage) bool {
	if len(images) > 4 {
		return false
	}
	total := 0
	for _, image := range images {
		prefix, encoded, ok := strings.Cut(image.DataURL, ",")
		if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64" && prefix != "data:image/webp;base64") {
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded) > 2<<20 {
			return false
		}
		if http.DetectContentType(decoded) != strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64") {
			return false
		}
		total += len(decoded)
	}
	return total <= 4<<20
}

func (a *API) interruptTurn(w http.ResponseWriter, r *http.Request) {
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
