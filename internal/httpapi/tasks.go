package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"github.com/wio-platform/wio/internal/task"
)

func (a *API) registerTaskRoutes(r chi.Router) {
	r.Post("/tasks", a.createTask)
	r.Get("/tasks", a.listTasks)
	r.Get("/tasks/recent", a.recentTasks)
	r.Get("/tasks/{taskID}", a.getTask)
	r.Get("/tasks/{taskID}/location", a.taskLocation)
	r.Get("/tasks/{taskID}/artifacts", a.taskArtifacts)
	r.Get("/tasks/{taskID}/reviews", a.taskReviews)
	r.Get("/tasks/{taskID}/team", a.taskTeam)
	r.Get("/tasks/{taskID}/plans", a.taskPlans)
	r.Get("/tasks/{taskID}/plans/{planID}", a.taskPlan)
	r.Post("/tasks/{taskID}/reviews", a.decideTaskReview)
	r.Post("/tasks/{taskID}/artifacts", a.captureTaskArtifact)
	r.Post("/tasks/{taskID}/artifacts/{artifactID}/handoffs", a.handoffTaskArtifact)
	r.Patch("/tasks/{taskID}", a.updateTask)
	r.Post("/tasks/{taskID}/plans", a.appendTaskPlan)
	r.Get("/tasks/{taskID}/events", a.taskEvents)
	r.Get("/task-requests/{requestID}", a.taskRequest)
	r.Put("/tasks/{taskID}/grant", a.setTaskGrant)
	r.Post("/tasks/{taskID}/runs", a.startTaskRun)
	r.Post("/tasks/{taskID}/runs/{runID}/actions", a.actOnTaskRun)
	r.Post("/tasks/{taskID}/runs/{runID}/acceptance", a.acceptTaskRun)
	r.Post("/tasks/{taskID}/archive", a.archiveTask)
}
func taskError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "INTERNAL_ERROR"
	message := "could not process task request"
	var e *task.Error
	if errors.As(err, &e) {
		code = e.Code
		message = e.Message
		switch e.Code {
		case task.Invalid:
			status = 400
		case task.Conflict, task.StateConflict, task.IdempotencyConflict:
			status = 409
		case task.Unsupported:
			status = 422
		case task.Forbidden:
			status = 403
		}
	}
	if store.IsNotFound(err) {
		status = 404
		code = "NOT_FOUND"
		message = "task or project resource not found"
	}
	writeJSON(w, status, map[string]any{"error": message, "code": code})
}
func decodeTaskBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		taskError(w, task.Fail(task.Invalid, "invalid task JSON or request body exceeds 1 MiB"))
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		taskError(w, task.Fail(task.Invalid, "provide exactly one JSON object"))
		return false
	}
	return true
}
func taskProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	ids := r.URL.Query()["project_id"]
	if len(ids) != 1 || ids[0] == "" {
		taskError(w, task.Fail(task.Invalid, "one project_id query parameter is required"))
		return "", false
	}
	return ids[0], true
}
func taskNumber(r *http.Request, name string, fallback, min, max int64) (int64, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, task.Fail(task.Invalid, "duplicate "+name)
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || n < min || n > max {
		return 0, task.Fail(task.Invalid, "invalid "+name)
	}
	return n, nil
}
func (a *API) publishTask(id, kind string) {
	a.hub.Publish(protocol.StreamEvent{StreamID: "task:" + id, Kind: kind, OccurredAt: time.Now().UTC()})
}
func (a *API) createTask(w http.ResponseWriter, r *http.Request) {
	var in task.Create
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	result, err := a.tasks.Create(r.Context(), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(result.ID, "task.created")
	writeJSON(w, 201, map[string]any{"task": result})
}
func (a *API) updateTask(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.Update
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	result, err := a.tasks.Update(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(result.ID, "task.spec.updated")
	writeJSON(w, 200, map[string]any{"task": result})
}
func (a *API) appendTaskPlan(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.AppendPlan
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	result, err := a.tasks.AppendPlan(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(result.Plan.TaskID, "task.plan.created")
	writeJSON(w, 201, result)
}
func (a *API) getTask(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	result, err := a.tasks.Get(r.Context(), project, chi.URLParam(r, "taskID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	limit, err := taskNumber(r, "limit", 50, 1, 100)
	if err != nil {
		taskError(w, err)
		return
	}
	for _, key := range []string{"state", "cursor"} {
		if len(r.URL.Query()[key]) > 1 {
			taskError(w, task.Fail(task.Invalid, "duplicate "+key))
			return
		}
	}
	result, err := a.tasks.List(r.Context(), task.Filter{ProjectID: project, State: r.URL.Query().Get("state"), Cursor: r.URL.Query().Get("cursor"), Limit: int(limit)})
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) taskEvents(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	after, err := taskNumber(r, "after", 0, 0, 1<<63-1)
	if err != nil {
		taskError(w, err)
		return
	}
	limit, err := taskNumber(r, "limit", 100, 1, 500)
	if err != nil {
		taskError(w, err)
		return
	}
	result, err := a.tasks.Events(r.Context(), project, chi.URLParam(r, "taskID"), after, int(limit))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *API) taskRequest(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	result, err := a.tasks.Request(r.Context(), currentSession(r).UserID, project, chi.URLParam(r, "requestID"))
	if err != nil {
		taskError(w, err)
		return
	}
	// Reconcile the same response shape the command originally returned.
	switch {
	case result.Action == "create" || result.Action == "update" || result.Action == "archive":
		result.Response = json.RawMessage(append(append([]byte(`{"task":`), result.Response...), '}'))
	case strings.HasPrefix(result.Action, "acceptance:"):
		result.Response = json.RawMessage(append(append([]byte(`{"run":`), result.Response...), '}'))
	}
	writeJSON(w, 200, result)
}

func (a *API) setTaskGrant(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.GrantInput
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.SetGrant(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(out.Grant.TaskID, "task.grant.updated")
	writeJSON(w, 200, out)
}
func (a *API) startTaskRun(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.Start
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.Start(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(out.Run.TaskID, "task.run.created")
	writeJSON(w, 201, out)
}
func (a *API) actOnTaskRun(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.Action
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.Action(r.Context(), project, chi.URLParam(r, "taskID"), chi.URLParam(r, "runID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(out.Run.TaskID, "task.run.state.changed")
	writeJSON(w, 202, out)
}

func (a *API) taskLocation(w http.ResponseWriter, r *http.Request) {
	project, err := a.tasks.Project(r.Context(), chi.URLParam(r, "taskID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"project_id": project})
}
func (a *API) recentTasks(w http.ResponseWriter, r *http.Request) {
	items, err := a.tasks.Recent(r.Context())
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) acceptTaskRun(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.Acceptance
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.Accept(r.Context(), project, chi.URLParam(r, "taskID"), chi.URLParam(r, "runID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(out.TaskID, "task.acceptance.recorded")
	writeJSON(w, 200, map[string]any{"run": out})
}
func (a *API) archiveTask(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.Archive
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.Archive(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(out.ID, "task.archived")
	writeJSON(w, 200, map[string]any{"task": out})
}
