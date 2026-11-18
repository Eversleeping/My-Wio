package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/task"
)

func (a *API) taskReviews(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	out, err := a.tasks.Reviews(r.Context(), project, chi.URLParam(r, "taskID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *API) decideTaskReview(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	var in task.ReviewDecision
	if !decodeTaskBody(w, r, &in) {
		return
	}
	in.Actor = currentSession(r).UserID
	out, err := a.tasks.DecideReview(r.Context(), project, chi.URLParam(r, "taskID"), in)
	if err != nil {
		taskError(w, err)
		return
	}
	a.publishTask(out.TaskID, "task.review.recorded")
	writeJSON(w, 200, out)
}
