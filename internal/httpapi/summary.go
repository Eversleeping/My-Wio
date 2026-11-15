package httpapi

import (
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"time"
)

func (a *API) summary(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{}
	queries := map[string]string{
		"servers":     "SELECT COUNT(*) FROM servers WHERE revoked_at IS NULL",
		"online":      "SELECT COUNT(*) FROM servers WHERE revoked_at IS NULL AND status='online' AND last_seen_at>?",
		"projects":    "SELECT COUNT(*) FROM projects",
		"threads":     "SELECT (SELECT COUNT(*) FROM codex_threads) + (SELECT COUNT(*) FROM gemini_threads) + (SELECT COUNT(*) FROM claude_threads)",
		"deployments": "SELECT COUNT(*) FROM deployments WHERE status IN ('queued','preparing','running')",
		"alerts":      "SELECT COUNT(*) FROM alerts WHERE status='open'",
	}
	for key, query := range queries {
		var count int
		var err error
		if key == "online" {
			err = a.store.DB.GetContext(r.Context(), &count, a.store.Q(query), time.Now().UTC().Add(-store.ServerOnlineGracePeriod))
		} else {
			err = a.store.DB.GetContext(r.Context(), &count, query)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load dashboard")
			return
		}
		counts[key] = count
	}
	deployments, _ := a.store.ListDeployments(r.Context())
	alerts, _ := a.store.ListAlerts(r.Context())
	if len(deployments) > 5 {
		deployments = deployments[:5]
	}
	if len(alerts) > 5 {
		alerts = alerts[:5]
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "deployments": deployments, "alerts": alerts})
}
