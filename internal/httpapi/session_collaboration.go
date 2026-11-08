package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

func collaborationRef(r *http.Request) sessiontools.Ref {
	return sessiontools.Ref{Engine: chi.URLParam(r, "engine"), ID: chi.URLParam(r, "threadID")}
}

func (a *API) markSessionCollaborationRead(w http.ResponseWriter, r *http.Request) {
	ref := collaborationRef(r)
	var input struct {
		Target string `json:"target_key"`
		Event  string `json:"event_id"`
	}
	if !ref.Valid() {
		writeError(w, 400, "invalid session identity")
		return
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := a.store.MarkCollaborationRead(r.Context(), ref, input.Target, input.Event); err != nil {
		writeError(w, 500, "could not mark collaboration result read")
		return
	}
	a.sessionCollaboration(w, r)
}

func (a *API) sessionCollaboration(w http.ResponseWriter, r *http.Request) {
	ref := collaborationRef(r)
	if !ref.Valid() {
		writeError(w, 400, "invalid session identity")
		return
	}
	state, err := a.store.ReadCollaborationState(r.Context(), ref)
	if err != nil {
		writeError(w, 404, "session not found")
		return
	}
	writeJSON(w, 200, state)
}

func (a *API) updateSessionCollaboration(w http.ResponseWriter, r *http.Request) {
	ref := collaborationRef(r)
	if !ref.Valid() {
		writeError(w, 400, "invalid session identity")
		return
	}
	var grant sessiontools.Grant
	if !decodeJSON(w, r, &grant) {
		return
	}
	if err := a.store.SetCollaborationGrant(r.Context(), ref, grant); err != nil {
		var detail *sessiontools.Error
		if errors.As(err, &detail) {
			writeJSON(w, 400, map[string]string{"code": detail.Code, "error": detail.Message})
		} else {
			writeError(w, 400, "could not update session collaboration")
		}
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "session.collaboration.grant", "session", ref.Key(), grant, clientIP(r))
	a.hub.Publish(protocol.StreamEvent{StreamID: ref.ID, Kind: "session.collaboration.grant.updated"})
	a.sessionCollaboration(w, r)
}
