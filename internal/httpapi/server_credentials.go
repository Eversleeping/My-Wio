package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

type serverCredentialProfilesInput struct {
	CodexProfileID string `json:"codex_profile_id"`
	GitProfileID   string `json:"git_profile_id"`
}

func (a *API) updateServerCredentialProfiles(w http.ResponseWriter, r *http.Request) {
	var input serverCredentialProfilesInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.CodexProfileID = strings.TrimSpace(input.CodexProfileID)
	input.GitProfileID = strings.TrimSpace(input.GitProfileID)
	if input.CodexProfileID == "" {
		writeError(w, http.StatusBadRequest, "Codex credential profile is required")
		return
	}
	serverID := chi.URLParam(r, "serverID")
	server, err := a.store.Server(r.Context(), serverID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server must be online to update credentials")
		return
	}
	active, err := a.store.HasActiveOperation(r.Context(), serverID, "credentials.configure")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not check credential update state")
		return
	}
	if active {
		writeError(w, http.StatusConflict, "a credential update is already queued for this server")
		return
	}
	resolved, err := a.resolveBootstrapCredentialProfiles(r.Context(), sshBootstrapInput{CodexProfileID: input.CodexProfileID, GitProfileID: input.GitProfileID})
	if err != nil || resolved.CodexAPIKey == "" {
		writeError(w, http.StatusBadRequest, "the selected credential profile is unavailable or invalid")
		return
	}
	command := protocol.ConfigureCredentialsCommand{
		CodexAPIURL: resolved.CodexAPIURL,
		CodexAPIKey: resolved.CodexAPIKey,
		CodexModel:  resolved.CodexModel,
		GitEndpoint: resolved.GitEndpoint,
		GitUsername: resolved.GitUsername,
		GitToken:    resolved.GitToken,
		RemoveGit:   input.GitProfileID == "",
	}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect credential update")
		return
	}
	operationID, err := a.store.QueueCredentialUpdate(r.Context(), serverID, ciphertext, input.CodexProfileID, input.GitProfileID, "credentials:"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue credential update")
		return
	}
	a.gateway.Wake(serverID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.credentials.update", "server", serverID, map[string]any{
		"operation_id": operationID, "codex_profile_id": input.CodexProfileID, "codex_profile_name": resolved.CodexProfileName,
		"git_profile_id": input.GitProfileID, "git_profile_name": resolved.GitProfileName, "remove_git": command.RemoveGit,
	}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "status": "queued"})
}
