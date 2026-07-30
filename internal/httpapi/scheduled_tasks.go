package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/schedule"
	"github.com/wio-platform/wio/internal/store"
)

type scheduledTaskRequest struct {
	ThreadID        *string `json:"thread_id"`
	Name            *string `json:"name"`
	Prompt          *string `json:"prompt"`
	Schedule        *string `json:"schedule"`
	Timezone        *string `json:"timezone"`
	Enabled         *bool   `json:"enabled"`
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	ApprovalMode    *string `json:"approval_mode"`
}

func (a *API) scheduledTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := a.store.ListScheduledTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list scheduled Codex tasks")
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (a *API) createScheduledTask(w http.ResponseWriter, r *http.Request) {
	var request scheduledTaskRequest
	if !decodeJSONLimit(w, r, &request, 256<<10) {
		return
	}
	input, err := a.normalizeScheduledTaskRequest(r, request, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	next, err := schedule.Next(input.Schedule, input.Timezone, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := a.store.CreateScheduledTask(r.Context(), input, next)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Codex session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create scheduled Codex task")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.schedule.create", "scheduled_task", task.ID, map[string]string{"thread_id": task.ThreadID}, clientIP(r))
	writeJSON(w, http.StatusCreated, task)
}

func (a *API) updateScheduledTask(w http.ResponseWriter, r *http.Request) {
	task, err := a.store.ScheduledTask(r.Context(), chi.URLParam(r, "taskID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "scheduled Codex task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load scheduled Codex task")
		return
	}
	var request scheduledTaskRequest
	if !decodeJSONLimit(w, r, &request, 256<<10) {
		return
	}
	input, err := a.normalizeScheduledTaskRequest(r, request, &task)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	next, err := schedule.Next(input.Schedule, input.Timezone, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := a.store.UpdateScheduledTask(r.Context(), task.ID, input, next)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "scheduled Codex task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update scheduled Codex task")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.schedule.update", "scheduled_task", task.ID, nil, clientIP(r))
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) deleteScheduledTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskID")
	if err := a.store.DeleteScheduledTask(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "scheduled Codex task not found")
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete scheduled Codex task")
	} else {
		session := currentSession(r)
		_ = a.store.Audit(r.Context(), session.UserID, "codex.schedule.delete", "scheduled_task", id, nil, clientIP(r))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) normalizeScheduledTaskRequest(r *http.Request, request scheduledTaskRequest, existing *store.ScheduledTask) (store.ScheduledTaskInput, error) {
	value := func(next *string, fallback string) string {
		if next != nil {
			return strings.TrimSpace(*next)
		}
		return fallback
	}
	threadID := value(request.ThreadID, "")
	name := value(request.Name, "")
	prompt := value(request.Prompt, "")
	expression := value(request.Schedule, "")
	timezone := value(request.Timezone, "UTC")
	model := value(request.Model, "")
	reasoning := value(request.ReasoningEffort, "")
	approval := value(request.ApprovalMode, "on-request")
	if existing != nil {
		if request.ThreadID == nil {
			threadID = existing.ThreadID
		}
		if request.Name == nil {
			name = existing.Name
		}
		if request.Prompt == nil {
			prompt = existing.Prompt
		}
		if request.Schedule == nil {
			expression = existing.Schedule
		}
		if request.Timezone == nil {
			timezone = existing.Timezone
		}
		if request.Model == nil {
			model = existing.Model
		}
		if request.ReasoningEffort == nil {
			reasoning = existing.ReasoningEffort
		}
		if request.ApprovalMode == nil {
			approval = existing.ApprovalMode
		}
	}
	if timezone == "" {
		timezone = "UTC"
	}
	enabled := true
	if existing != nil {
		enabled = existing.Enabled
	}
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	if threadID == "" || name == "" || prompt == "" || expression == "" {
		return store.ScheduledTaskInput{}, errors.New("thread_id, name, prompt, and schedule are required")
	}
	if utf8.RuneCountInString(name) > 180 {
		return store.ScheduledTaskInput{}, errors.New("name must contain at most 180 characters")
	}
	if utf8.RuneCountInString(prompt) > 20000 {
		return store.ScheduledTaskInput{}, errors.New("prompt must contain at most 20000 characters")
	}
	if utf8.RuneCountInString(expression) > 100 {
		return store.ScheduledTaskInput{}, errors.New("schedule must contain at most 100 characters")
	}
	if utf8.RuneCountInString(model) > 128 {
		return store.ScheduledTaskInput{}, errors.New("model is too long")
	}
	if reasoning != "" && !validReasoningEffort(reasoning) {
		return store.ScheduledTaskInput{}, errors.New("invalid reasoning_effort")
	}
	if approval != "on-request" && approval != "untrusted" && approval != "never" {
		return store.ScheduledTaskInput{}, errors.New("invalid approval_mode")
	}
	if _, err := a.store.Thread(r.Context(), threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ScheduledTaskInput{}, errors.New("Codex session not found")
		}
		return store.ScheduledTaskInput{}, errors.New("could not load Codex session")
	}
	return store.ScheduledTaskInput{ThreadID: threadID, Name: name, Prompt: prompt, Schedule: expression, Timezone: timezone, Enabled: enabled, Model: model, ReasoningEffort: reasoning, ApprovalMode: approval}, nil
}
