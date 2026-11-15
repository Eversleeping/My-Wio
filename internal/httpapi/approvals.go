package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"time"
)

type approvalView struct {
	ID         string          `db:"id" json:"id"`
	ThreadID   string          `db:"thread_id" json:"thread_id"`
	RequestID  string          `db:"request_id" json:"request_id"`
	Kind       string          `db:"kind" json:"kind"`
	Detail     json.RawMessage `db:"-" json:"detail"`
	DetailText string          `db:"detail" json:"-"`
	Status     string          `db:"status" json:"status"`
	Title      string          `db:"title" json:"title"`
	ExpiresAt  time.Time       `db:"expires_at" json:"expires_at"`
}

func (a *API) approvals(w http.ResponseWriter, r *http.Request) {
	var rows []approvalView
	err := a.store.DB.SelectContext(r.Context(), &rows, `SELECT a.id,a.thread_id,a.request_id,a.kind,a.detail,a.status,t.title,a.expires_at FROM approvals a JOIN codex_threads t ON t.id=a.thread_id WHERE a.status='pending' ORDER BY a.expires_at`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list approvals")
		return
	}
	for index := range rows {
		rows[index].Detail = json.RawMessage(rows[index].DetailText)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (a *API) decideApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Decision string                              `json:"decision"`
		Answers  map[string]protocol.UserInputAnswer `json:"answers"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Decision != "approved" && input.Decision != "denied" && input.Decision != "answered" {
		writeError(w, http.StatusBadRequest, "decision must be approved or denied")
		return
	}
	var approval struct {
		ID        string `db:"id"`
		ThreadID  string `db:"thread_id"`
		RequestID string `db:"request_id"`
		ServerID  string `db:"server_id"`
		Kind      string `db:"kind"`
		Detail    string `db:"detail"`
	}
	err := a.store.DB.GetContext(r.Context(), &approval, a.store.Q(`SELECT a.id,a.thread_id,a.request_id,a.kind,a.detail,w.server_id FROM approvals a JOIN codex_threads t ON t.id=a.thread_id JOIN workspaces w ON w.id=t.workspace_id WHERE a.id=? AND a.status='pending' AND a.expires_at>?`), chi.URLParam(r, "approvalID"), time.Now().UTC())
	if err != nil {
		writeStoreError(w, err, "could not load pending approval")
		return
	}
	if approval.Kind == protocol.UserInputRequestMethod {
		if input.Decision != "answered" {
			writeError(w, http.StatusBadRequest, "user input requires answers")
			return
		}
		if err := protocol.ValidateUserInputAnswers(json.RawMessage(approval.Detail), input.Answers); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else if input.Decision == "answered" || len(input.Answers) > 0 {
		writeError(w, http.StatusBadRequest, "approval requires an approval decision")
		return
	}
	command := protocol.ApprovalDecisionCommand{ThreadID: approval.ThreadID, RequestID: approval.RequestID, Decision: input.Decision, Answers: input.Answers}
	operationID, err := a.store.ResolveApprovalAndQueue(r.Context(), approval.ID, approval.ServerID, input.Decision, command)
	if errors.Is(err, store.ErrApprovalResolved) {
		writeError(w, http.StatusConflict, "approval was already resolved")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not send approval decision")
		return
	}
	a.gateway.Wake(approval.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "codex.approval.decide", "approval", approval.ID, map[string]string{"decision": input.Decision}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
}
