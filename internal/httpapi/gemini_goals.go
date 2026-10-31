package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) geminiGoal(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	goal, err := a.store.GeminiGoal(r.Context(), thread.ID)
	if err != nil {
		writeError(w, 500, "could not load Gemini goal")
		return
	}
	writeJSON(w, 200, goal)
}

func (a *API) controlGeminiGoal(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadGeminiThread(w, r)
	if !ok {
		return
	}
	if thread.ArchivedAt != nil {
		writeError(w, 409, "archived Gemini sessions are read-only")
		return
	}
	goal, err := a.store.GeminiGoal(r.Context(), thread.ID)
	if err != nil {
		writeError(w, 500, "could not load Gemini goal")
		return
	}
	if goal == nil || goal.ID != chi.URLParam(r, "goalID") {
		writeError(w, 409, "Gemini goal changed")
		return
	}
	action := chi.URLParam(r, "action")
	switch action {
	case "pause":
		if goal.Status != "active" || a.store.InterruptGeminiTurn(r.Context(), thread, "paused", goal.ID) != nil {
			writeError(w, 409, "Gemini goal is no longer running")
			return
		}
	case "cancel":
		if goal.Status == "active" {
			err = a.store.InterruptGeminiTurn(r.Context(), thread, "cancelled", goal.ID)
		} else {
			err = a.store.CancelInactiveGeminiGoal(r.Context(), thread.ID, goal.ID)
		}
		if err != nil {
			writeError(w, 409, "Gemini goal changed or cannot be cancelled")
			return
		}
	case "resume":
		if thread.ServerStatus != "online" || thread.ArchivedAt != nil || !buildinfo.SupportsGeminiGoal(thread.AgentVersion, thread.CLIVersion) {
			writeError(w, 409, "Gemini goal requires an online, compatible Agent and an unarchived session")
			return
		}
		var input struct {
			MaxContinuations int `json:"max_continuations"`
			MaxSeconds       int `json:"max_seconds"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if !store.ValidGeminiGoalLimits(input.MaxContinuations, input.MaxSeconds) {
			writeError(w, 400, "invalid Gemini goal limits")
			return
		}
		command := &protocol.GeminiGoalCommand{ID: goal.ID, MaxContinuations: input.MaxContinuations, MaxSeconds: input.MaxSeconds}
		event, err := a.store.QueueGeminiGoal(r.Context(), thread, "Continue the active goal and verify the remaining work.", thread.Model, thread.ApprovalMode, thread.ThinkingLevel, nil, command)
		if err != nil {
			writeError(w, 409, "Gemini goal cannot be resumed while the session or workspace is active")
			return
		}
		a.hub.Publish(event)
	default:
		writeError(w, 404, "unknown Gemini goal action")
		return
	}
	a.gateway.Wake(thread.ServerID)
	a.auditGemini(r, "gemini.goal."+action, thread.ID)
	a.publishGeminiChange(r, thread.ID, "gemini.thread.updated")
	writeJSON(w, 202, map[string]bool{"ok": true})
}
