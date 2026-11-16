package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

type credentialSpec struct {
	kind, name, minimum string
	capability          string
	busy                error
	command             func(store.CredentialProfile, string) any
	queue               func(context.Context, string, store.CredentialProfile, string) (string, error)
}

func (a *API) updateServerClaudeCredentialProfile(w http.ResponseWriter, r *http.Request) {
	a.updateServerEngineCredentialProfile(w, r, credentialSpec{
		kind: "claude", name: "Claude", minimum: buildinfo.MinimumAgentVersion, capability: protocol.CapabilityClaudeManage, busy: store.ErrClaudeCredentialsBusy,
		command: func(p store.CredentialProfile, key string) any {
			return protocol.ConfigureClaudeCredentialsCommand{Endpoint: p.Endpoint, APIKey: key, Model: p.Model}
		},
		queue: func(ctx context.Context, server string, p store.CredentialProfile, ciphertext string) (string, error) {
			return a.store.QueueClaudeCredentialUpdate(ctx, server, p.ID, ciphertext, p.Model, p.UpdatedAt)
		},
	})
}
func (a *API) updateServerGeminiCredentialProfile(w http.ResponseWriter, r *http.Request) {
	a.updateServerEngineCredentialProfile(w, r, credentialSpec{
		kind: "gemini", name: "Gemini", minimum: buildinfo.MinimumAgentVersion, capability: protocol.CapabilityGeminiCredentials, busy: store.ErrGeminiCredentialsBusy,
		command: func(p store.CredentialProfile, key string) any {
			return protocol.ConfigureGeminiCredentialsCommand{Endpoint: p.Endpoint, APIKey: key, Model: p.Model}
		},
		queue: func(ctx context.Context, server string, p store.CredentialProfile, ciphertext string) (string, error) {
			return a.store.QueueGeminiCredentialUpdate(ctx, server, p.ID, ciphertext, p.UpdatedAt)
		},
	})
}
func (a *API) updateServerEngineCredentialProfile(w http.ResponseWriter, r *http.Request, cfg credentialSpec) {
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
		writeError(w, 409, "server must be online to update "+cfg.name+" credentials")
		return
	}
	if !server.Supports(cfg.capability) {
		writeError(w, 409, "update Wio Agent to "+cfg.minimum+" or newer first")
		return
	}
	profile, err := a.store.CredentialProfile(r.Context(), strings.TrimSpace(input.ProfileID))
	if err != nil {
		writeStoreError(w, err, "could not load credential profile")
		return
	}
	if profile.Kind != cfg.kind {
		writeError(w, 400, "select a valid "+cfg.name+" API profile")
		return
	}
	var key string
	if a.vault == nil || a.vault.Decrypt(profile.Ciphertext, &key) != nil || !validCredentialSecret(key) {
		writeError(w, 400, cfg.name+" API profile is unavailable")
		return
	}
	command := cfg.command(profile, key)
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, 500, "could not protect "+cfg.name+" credentials")
		return
	}
	id, err := cfg.queue(r.Context(), server.ID, profile, ciphertext)
	if errors.Is(err, cfg.busy) {
		writeError(w, 409, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "could not queue "+cfg.name+" credentials")
		return
	}
	a.gateway.Wake(server.ID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "server."+cfg.kind+".credentials.update", "server", server.ID, map[string]string{"operation_id": id, "profile_id": profile.ID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": id, "status": "queued"})
}
