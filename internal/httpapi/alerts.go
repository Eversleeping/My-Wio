package httpapi

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"time"
)

type auditListPageResponse struct {
	Items   []auditEntry `json:"items"`
	HasMore bool         `json:"has_more"`
	Next    *int         `json:"next"`
}

func (a *API) alerts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := parseThreadPageValue(query["limit"], query.Has("limit"), "limit", 200, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	offset, err := parseThreadPageValue(query["offset"], query.Has("offset"), "offset", 0, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	alerts, err := a.store.ListAlertsPage(r.Context(), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list alerts")
		return
	}
	writeJSON(w, http.StatusOK, alerts)
}

func (a *API) acknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Q("UPDATE alerts SET acknowledged_at=? WHERE id=?"), time.Now().UTC(), chi.URLParam(r, "alertID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not acknowledge alert")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeError(w, http.StatusNotFound, "alert not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type auditEntry struct {
	ID           string          `db:"id" json:"id"`
	Action       string          `db:"action" json:"action"`
	ResourceType string          `db:"resource_type" json:"resource_type"`
	ResourceID   string          `db:"resource_id" json:"resource_id"`
	Detail       json.RawMessage `db:"-" json:"detail"`
	DetailText   string          `db:"detail" json:"-"`
	IPAddress    string          `db:"ip_address" json:"ip_address"`
	OccurredAt   time.Time       `db:"occurred_at" json:"occurred_at"`
}

func (a *API) auditLog(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limitValues, hasLimit := query["limit"]
	offsetValues, hasOffset := query["offset"]
	if hasLimit || hasOffset {
		limit, err := parseThreadPageValue(limitValues, hasLimit, "limit", defaultAuditPageLimit, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if limit > maxAuditPageLimit {
			limit = maxAuditPageLimit
		}
		offset, err := parseThreadPageValue(offsetValues, hasOffset, "offset", 0, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := a.store.ListAuditPage(r.Context(), limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not list audit log")
			return
		}
		entries := makeAuditEntries(page.Items)
		var next *int
		if page.HasMore {
			value := offset + len(entries)
			next = &value
		}
		writeJSON(w, http.StatusOK, auditListPageResponse{Items: entries, HasMore: page.HasMore, Next: next})
		return
	}
	var entries []auditEntry
	err := a.store.DB.SelectContext(r.Context(), &entries, "SELECT id,action,resource_type,resource_id,detail,ip_address,occurred_at FROM audit_log ORDER BY occurred_at DESC LIMIT 500")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list audit log")
		return
	}
	for index := range entries {
		entries[index].Detail = json.RawMessage(entries[index].DetailText)
	}
	writeJSON(w, http.StatusOK, entries)
}

func makeAuditEntries(records []store.AuditRecord) []auditEntry {
	entries := make([]auditEntry, len(records))
	for index, record := range records {
		entries[index] = auditEntry{ID: record.ID, Action: record.Action, ResourceType: record.ResourceType, ResourceID: record.ResourceID, Detail: json.RawMessage(record.Detail), IPAddress: record.IPAddress, OccurredAt: record.OccurredAt}
	}
	return entries
}
