package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/session"
)

func writeUnsupportedSessionAction(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]string{"code": "CAPABILITY_UNSUPPORTED", "error": "session action is not supported"})
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, key := range []string{"engine", "archived", "q", "limit", "cursor"} {
		if len(q[key]) > 1 {
			writeError(w, 400, "duplicate session filter")
			return
		}
	}
	active := false
	filter := session.Filter{Engine: q.Get("engine"), Archived: &active, Query: strings.TrimSpace(q.Get("q")), Cursor: q.Get("cursor"), Limit: 50}
	if filter.Engine != "" && !session.ValidEngine(filter.Engine) {
		writeError(w, 400, "invalid session engine")
		return
	}
	switch q.Get("archived") {
	case "", "false":
	case "true":
		archived := true
		filter.Archived = &archived
	case "all":
		filter.Archived = nil
	default:
		writeError(w, 400, "archived must be false, true or all")
		return
	}
	if q.Has("offset") {
		writeError(w, 400, "use a session cursor instead of offset")
		return
	}
	if q.Has("limit") {
		limit, err := parseThreadPageValue(q["limit"], true, "limit", 50, true)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if limit > 100 {
			limit = 100
		}
		filter.Limit = limit
	}
	page, err := a.sessionReader().List(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err, "could not list sessions")
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		driver, e := a.sessionDriver(item.Engine)
		if e != nil {
			writeError(w, 500, "could not resolve session driver")
			return
		}
		response, e := sessionResponse(r.Context(), driver, item)
		if e != nil {
			writeStoreError(w, e, "could not load session")
			return
		}
		items = append(items, response)
	}
	writeJSON(w, 200, struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
		Groups     []session.Group  `json:"groups"`
	}{items, page.NextCursor, page.Groups})
}

func sessionResponse(ctx context.Context, driver session.Driver, item session.Record) (map[string]any, error) {
	native, err := driver.Native(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(item)
	if err != nil {
		return nil, err
	}
	// Unified clients use the capability object. Native fields remain internal
	// to the drivers while their historical HTTP aliases are retired.
	for key := range result {
		if strings.HasSuffix(key, "_supported") {
			delete(result, key)
		}
	}
	// Merge the common fields last so grouping and ordering use one definition.
	err = json.Unmarshal(raw, &result)
	return result, err
}

func sessionActionSupported(c session.Capabilities, action string) bool {
	checks := map[string]bool{"turns": c.Turns, "rewrite": c.Rewrite, "steer": c.Steer, "confirm-stopped": c.ConfirmStopped, "fork": c.Fork, "fork-worktree": c.ForkWorktree, "answer": c.Questions, "goal:get": c.Goal, "goal:set": c.Goal, "goal:clear": c.Goal, "goal:refresh": c.Goal, "goal:control": c.Goal, "compact": c.Compact}
	value, present := checks[action]
	return !present || value
}

func (a *API) unifiedSessionAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		driver, err := a.sessionDriver(chi.URLParam(r, "engine"))
		if err != nil {
			writeError(w, 400, "invalid session engine")
			return
		}
		handler := driver.Handler(action)
		if handler == nil && action != "detail" && action != "events" {
			writeUnsupportedSessionAction(w)
			return
		}
		var item session.Record
		if action != "create" {
			item, err = driver.Session(r.Context(), chi.URLParam(r, "threadID"))
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, 404, "session not found")
				return
			}
			if err != nil {
				writeStoreError(w, err, "could not load session")
				return
			}
			if !sessionActionSupported(item.Capabilities, action) {
				writeUnsupportedSessionAction(w)
				return
			}
		}
		if action == "detail" {
			result, e := sessionResponse(r.Context(), driver, item)
			if e != nil {
				writeStoreError(w, e, "could not load session")
				return
			}
			writeJSON(w, 200, result)
			return
		}
		if action == "events" {
			serveSessionEvents(w, r, driver, item.ID)
			return
		}
		if action != "create" && action != "update" {
			handler(w, r)
			return
		}
		response := &sessionActionResponse{header: make(http.Header)}
		handler(response, r)
		if response.status >= 200 && response.status < 300 {
			id := item.ID
			if action == "create" {
				var created struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(response.body.Bytes(), &created) != nil || created.ID == "" {
					writeError(w, 500, "could not load created session")
					return
				}
				id = created.ID
			}
			item, err = driver.Session(r.Context(), id)
			if err != nil {
				writeStoreError(w, err, "could not load session")
				return
			}
			result, e := sessionResponse(r.Context(), driver, item)
			if e != nil {
				writeStoreError(w, e, "could not load session")
				return
			}
			writeJSON(w, response.status, result)
			return
		}
		for name, values := range response.header {
			w.Header()[name] = values
		}
		w.WriteHeader(response.status)
		_, _ = w.Write(response.body.Bytes())
	}
}

// Buffer native JSON create/update actions to return the common read model.
type sessionActionResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *sessionActionResponse) Header() http.Header { return r.header }
func (r *sessionActionResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *sessionActionResponse) Write(raw []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.body.Write(raw)
}

func serveSessionEvents(w http.ResponseWriter, r *http.Request, driver session.Driver, id string) {
	q := r.URL.Query()
	filter := session.EventQuery{Limit: 500, Raw: q.Get("view") == "raw"}
	if q.Has("after") && q.Has("before") {
		writeError(w, 400, "before and after cannot be used together")
		return
	}
	for _, key := range []string{"after", "before"} {
		if !q.Has(key) {
			continue
		}
		if len(q[key]) != 1 || q.Get(key) == "" {
			writeError(w, 400, "invalid event cursor")
			return
		}
		value, err := strconv.ParseInt(q.Get(key), 10, 64)
		if err != nil || value < 0 {
			writeError(w, 400, "invalid event cursor")
			return
		}
		if key == "after" {
			filter.After = &value
		} else {
			filter.Before = &value
		}
	}
	if q.Has("limit") {
		limit, err := parseThreadPageValue(q["limit"], true, "limit", 500, true)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if limit > 1000 {
			limit = 1000
		}
		filter.Limit = limit
	}
	if len(q["view"]) > 1 || (q.Get("view") != "" && q.Get("view") != "raw" && q.Get("view") != "conversation") {
		writeError(w, 400, "invalid event view")
		return
	}
	events, err := driver.Events(r.Context(), id, filter)
	if err != nil {
		writeStoreError(w, err, "could not load session events")
		return
	}
	writeJSON(w, 200, events)
}
