package httpapi

import "github.com/go-chi/chi/v5"

func (api *API) registerUnifiedSessionRoutes(private chi.Router) {
	private.Get("/sessions", api.listSessions)
	private.Post("/sessions/{engine}", api.unifiedSessionAction("create"))
	base := "/sessions/{engine}/{threadID}"
	private.Get(base, api.unifiedSessionAction("detail"))
	private.Patch(base, api.unifiedSessionAction("update"))
	private.Delete(base, api.unifiedSessionAction("delete"))
	private.Get(base+"/events", api.unifiedSessionAction("events"))
	private.Get(base+"/usage", api.unifiedSessionAction("usage"))
	for _, action := range []string{"turns", "steer", "interrupt", "confirm-stopped", "fork", "fork-worktree", "compact"} {
		private.Post(base+"/"+action, api.unifiedSessionAction(action))
	}
	private.Post(base+"/events/{eventID}/rewrite", api.unifiedSessionAction("rewrite"))
	private.Post(base+"/questions/{requestID}/answer", api.unifiedSessionAction("answer"))
	private.Get(base+"/goal", api.unifiedSessionAction("goal:get"))
	private.Put(base+"/goal", api.unifiedSessionAction("goal:set"))
	private.Delete(base+"/goal", api.unifiedSessionAction("goal:clear"))
	private.Post(base+"/goal/refresh", api.unifiedSessionAction("goal:refresh"))
	private.Post(base+"/goals/{goalID}/{action}", api.unifiedSessionAction("goal:control"))
	private.Get(base+"/status", api.unifiedSessionAction("status"))
	private.Post(base+"/status/refresh", api.unifiedSessionAction("status:refresh"))
}
