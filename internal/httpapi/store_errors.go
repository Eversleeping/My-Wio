package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wio-platform/wio/internal/store"
)

func writeStoreError(w http.ResponseWriter, err error, message string) {
	var pgError *pgconn.PgError
	var sqliteError interface{ Code() int }
	switch {
	case errors.As(err, &pgError) && (pgError.Code == "23505" || pgError.Code == "23503"):
		writeError(w, http.StatusConflict, "resource conflicts with existing data")
	case errors.As(err, &pgError) && (pgError.Code == "23502" || pgError.Code == "23514"):
		writeError(w, http.StatusBadRequest, "invalid resource data")
	case errors.As(err, &sqliteError) && (sqliteError.Code() == 2067 || sqliteError.Code() == 1555 || sqliteError.Code() == 787):
		writeError(w, http.StatusConflict, "resource conflicts with existing data")
	case errors.As(err, &sqliteError) && (sqliteError.Code() == 1299 || sqliteError.Code() == 275):
		writeError(w, http.StatusBadRequest, "invalid resource data")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, store.ErrStateConflict), errors.Is(err, store.ErrClaudeCredentialsBusy), errors.Is(err, store.ErrGeminiCredentialsBusy), errors.Is(err, store.ErrGeminiSkillsBusy), errors.Is(err, store.ErrGeminiGoalChanged), errors.Is(err, store.ErrProjectRetryConflict), errors.Is(err, store.ErrRequestConflict), errors.Is(err, store.ErrThreadActive), errors.Is(err, store.ErrWorkspaceWriteActive), errors.Is(err, store.ErrDeploymentActive), errors.Is(err, store.ErrDeploymentContainerActive):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrInvalidInput), errors.Is(err, store.ErrInvalidEditTarget), errors.Is(err, store.ErrInvalidWorkspaceGitStatus):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, message)
	}
}

// A missing workspace selected in a create payload is invalid input. Missing
// resources addressed by URL still use writeStoreError's 404 response.
func writeSessionCreateError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusBadRequest, "workspace not found")
		return
	}
	writeStoreError(w, err, message)
}

// Answer validation is relative to the pending question and retains the
// established conflict response; malformed request JSON is handled separately.
func writeQuestionAnswerError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, store.ErrInvalidInput) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeStoreError(w, err, message)
}

func (a *API) replayCodexRequest(w http.ResponseWriter, r *http.Request, requestID, action string, input store.CodexRequestInput) bool {
	if requestID == "" {
		return false
	}
	operationID, err := a.store.LookupCodexRequest(r.Context(), chi.URLParam(r, "threadID"), requestID, action, input)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		writeStoreError(w, err, "could not load previous Codex request")
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID})
	return true
}

// Normalizers also read resources; preserve those failures separately from
// ordinary validation messages instead of turning a database outage into 400.
type inputStoreFailure struct{ error }

func (e inputStoreFailure) Unwrap() error { return e.error }

func writeInputError(w http.ResponseWriter, err error) {
	var failure inputStoreFailure
	if errors.As(err, &failure) {
		writeStoreError(w, failure.error, "could not load request resources")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}
