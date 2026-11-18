package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/task"
)

func (a *API) taskArtifacts(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	out, err := a.tasks.Artifacts(r.Context(), project, chi.URLParam(r, "taskID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (a *API) captureTaskArtifact(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.CaptureArtifact
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.CaptureArtifact(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.wakeTaskArtifact(r, out.OperationID, out.TaskID, "task.artifact.created")
	writeJSON(w, 202, out)
}
func (a *API) handoffTaskArtifact(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.HandoffArtifact
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.HandoffArtifact(r.Context(), project, chi.URLParam(r, "taskID"), chi.URLParam(r, "artifactID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.wakeTaskArtifact(r, out.OperationID, out.TaskID, "task.artifact.handoff.changed")
	writeJSON(w, 202, out)
}
func (a *API) wakeTaskArtifact(r *http.Request, operation, id, event string) {
	if op, err := a.store.Operation(r.Context(), operation); err == nil {
		a.gateway.Wake(op.ServerID)
	}
	a.publishTask(id, event)
}
