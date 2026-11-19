package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) sessionSubagents(w http.ResponseWriter, r *http.Request) {
	ref := session.Ref{Engine: chi.URLParam(r, "engine"), ID: chi.URLParam(r, "threadID")}
	driver, err := a.sessionDriver(ref.Engine)
	if err != nil {
		writeError(w, 400, "invalid session engine")
		return
	}
	parent, err := driver.Session(r.Context(), ref.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "session not found")
		} else {
			writeStoreError(w, err, "could not load session")
		}
		return
	}
	items, err := a.store.ReadSubagents(r.Context(), ref)
	if err != nil {
		writeStoreError(w, err, "could not load subagents")
		return
	}
	for index := range items {
		unconfirmed := parent.ServerStatus != "online" || parent.RecoveryPending ||
			((parent.Status == "failed" || parent.Status == "interrupted") && !items[index].UpdatedAt.After(parent.UpdatedAt))
		if unconfirmed {
			if items[index].Status == "running" || items[index].Status == "queued" || items[index].Status == "waiting_user" {
				items[index].Status = "unknown"
			}
		}
	}
	id := chi.URLParam(r, "subagentID")
	if id == "" {
		writeJSON(w, 200, struct {
			Items []store.Subagent `json:"items"`
		}{items})
		return
	}
	found := false
	for _, item := range items {
		if item.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, 404, "subagent not found")
		return
	}
	after := int64(0)
	if value := r.URL.Query().Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			writeError(w, 400, "invalid event cursor")
			return
		}
	}
	events, err := a.store.SubagentEvents(r.Context(), ref.ID, id, after, 101)
	if err != nil {
		writeStoreError(w, err, "could not load subagent content")
		return
	}
	more := len(events) > 100
	if more {
		events = events[:100]
	}
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].Sequence
	}
	writeJSON(w, 200, map[string]any{"items": events, "next": next, "has_more": more})
}
