package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (a *API) taskTeam(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	items, err := a.tasks.Team(r.Context(), project, chi.URLParam(r, "taskID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) taskPlans(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	items, err := a.tasks.Plans(r.Context(), project, chi.URLParam(r, "taskID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *API) taskPlan(w http.ResponseWriter, r *http.Request) {
	project, ok := taskProject(w, r)
	if !ok {
		return
	}
	plan, err := a.tasks.Plan(r.Context(), project, chi.URLParam(r, "taskID"), chi.URLParam(r, "planID"))
	if err != nil {
		taskError(w, err)
		return
	}
	writeJSON(w, 200, plan)
}
