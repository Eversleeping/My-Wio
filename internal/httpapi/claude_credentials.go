package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func (a *API) updateServerClaudeCredentialProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProfileID string `json:"profile_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	server, err := a.store.Server(r.Context(), chi.URLParam(r, "serverID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "server not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, 409, "server must be online to update Claude credentials")
		return
	}
	if !buildinfo.SupportsClaudeManagement(server.AgentVersion) {
		writeError(w, 409, "update Wio Agent to "+buildinfo.MinimumClaudeManagementVersion+" or newer first")
		return
	}
	profile, err := a.store.CredentialProfile(r.Context(), strings.TrimSpace(input.ProfileID))
	if err != nil {
		writeStoreError(w, err, "could not load credential profile")
		return
	}
	if profile.Kind != "claude" {
		writeError(w, 400, "select a valid Claude API profile")
		return
	}
	var key string
	if a.vault == nil || a.vault.Decrypt(profile.Ciphertext, &key) != nil || !validCredentialSecret(key) {
		writeError(w, 400, "Claude API profile is unavailable")
		return
	}
	command := protocol.ConfigureClaudeCredentialsCommand{Endpoint: profile.Endpoint, APIKey: key, Model: profile.Model}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, 500, "could not protect Claude credentials")
		return
	}
	id, err := a.store.QueueClaudeCredentialUpdate(r.Context(), server.ID, profile.ID, ciphertext, profile.Model, profile.UpdatedAt)
	if errors.Is(err, store.ErrClaudeCredentialsBusy) {
		writeError(w, 409, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "could not queue Claude credentials")
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server.claude.credentials.update", "server", server.ID, map[string]string{"operation_id": id, "profile_id": profile.ID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": id, "status": "queued"})
}
