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

func (a *API) updateServerGeminiCredentialProfile(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, 409, "server must be online to update Gemini credentials")
		return
	}
	if !buildinfo.SupportsGeminiCredentials(server.AgentVersion) {
		writeError(w, 409, "update Wio Agent to "+buildinfo.MinimumGeminiCredentialsVersion+" or newer first")
		return
	}
	profile, err := a.store.CredentialProfile(r.Context(), strings.TrimSpace(input.ProfileID))
	if err != nil || profile.Kind != "gemini" {
		writeError(w, 400, "select a valid Gemini API profile")
		return
	}
	var key string
	if a.vault == nil || a.vault.Decrypt(profile.Ciphertext, &key) != nil || !validCredentialSecret(key) {
		writeError(w, 400, "Gemini API profile is unavailable")
		return
	}
	command := protocol.ConfigureGeminiCredentialsCommand{Endpoint: profile.Endpoint, APIKey: key, Model: profile.Model}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, 500, "could not protect Gemini credentials")
		return
	}
	id, err := a.store.QueueGeminiCredentialUpdate(r.Context(), server.ID, profile.ID, ciphertext)
	if errors.Is(err, store.ErrGeminiCredentialsBusy) {
		writeError(w, 409, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "could not queue Gemini credentials")
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server.gemini.credentials.update", "server", server.ID, map[string]string{"operation_id": id, "profile_id": profile.ID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": id, "status": "queued"})
}
