package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/gitidentity"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

type serverCredentialProfilesInput struct {
	CodexProfileID string   `json:"codex_profile_id"`
	GitProfileID   string   `json:"git_profile_id"`
	GitProfileIDs  []string `json:"git_profile_ids"`
}

func (a *API) updateServerCredentialProfiles(w http.ResponseWriter, r *http.Request) {
	var input serverCredentialProfilesInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.CodexProfileID = strings.TrimSpace(input.CodexProfileID)
	input.GitProfileIDs = normalizedProfileIDs(input.GitProfileIDs, input.GitProfileID)
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
	command, gitProfiles, err := a.resolveServerCredentialProfiles(r.Context(), input.CodexProfileID, input.GitProfileIDs)
	if err != nil || command.CodexAPIKey == "" {
		writeError(w, http.StatusBadRequest, "the selected credential profile is unavailable or invalid")
		return
	}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect credential update")
		return
	}
	operationID, err := a.store.QueueCredentialUpdate(r.Context(), serverID, ciphertext, input.CodexProfileID, input.GitProfileIDs, "credentials:"+store.NewID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue credential update")
		return
	}
	a.gateway.Wake(serverID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "server.credentials.update", "server", serverID, map[string]any{
		"operation_id": operationID, "codex_profile_id": input.CodexProfileID,
		"git_profile_ids": input.GitProfileIDs, "git_profile_names": credentialProfileNames(gitProfiles), "remove_git": command.RemoveGit,
	}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "status": "queued"})
}

func normalizedProfileIDs(ids []string, legacyID string) []string {
	if legacyID = strings.TrimSpace(legacyID); legacyID != "" {
		ids = append(ids, legacyID)
	}
	seen := make(map[string]bool, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized
}

func (a *API) resolveServerCredentialProfiles(ctx context.Context, codexProfileID string, gitProfileIDs []string) (protocol.ConfigureCredentialsCommand, []store.CredentialProfile, error) {
	if a.vault == nil {
		return protocol.ConfigureCredentialsCommand{}, nil, errCredentialProfile
	}
	codex, err := a.store.CredentialProfile(ctx, codexProfileID)
	if err != nil || codex.Kind != "codex" {
		return protocol.ConfigureCredentialsCommand{}, nil, errCredentialProfile
	}
	var codexAPIKey string
	if err := a.vault.Decrypt(codex.Ciphertext, &codexAPIKey); err != nil {
		return protocol.ConfigureCredentialsCommand{}, nil, errCredentialProfile
	}
	command := protocol.ConfigureCredentialsCommand{CodexAPIURL: codex.Endpoint, CodexAPIKey: codexAPIKey, CodexModel: codex.Model, RemoveGit: len(gitProfileIDs) == 0}
	gitProfiles := make([]store.CredentialProfile, 0, len(gitProfileIDs))
	for _, profileID := range gitProfileIDs {
		profile, err := a.store.CredentialProfile(ctx, profileID)
		if err != nil || profile.Kind != "git" {
			return protocol.ConfigureCredentialsCommand{}, nil, errCredentialProfile
		}
		if _, _, err := gitidentity.Normalize(profile.CommitName, profile.CommitEmail); err != nil {
			return protocol.ConfigureCredentialsCommand{}, nil, errCredentialProfile
		}
		var token string
		if err := a.vault.Decrypt(profile.Ciphertext, &token); err != nil {
			return protocol.ConfigureCredentialsCommand{}, nil, errCredentialProfile
		}
		credential := protocol.GitCredential{Endpoint: profile.Endpoint, Username: profile.Username, Token: token, CommitName: profile.CommitName, CommitEmail: profile.CommitEmail}
		command.GitCredentials = append(command.GitCredentials, credential)
		gitProfiles = append(gitProfiles, profile)
	}
	if len(command.GitCredentials) > 0 {
		// Keep the first profile in legacy fields for Agents that have not yet
		// updated to understand the multi-profile payload.
		primary := command.GitCredentials[0]
		command.GitEndpoint = primary.Endpoint
		command.GitUsername = primary.Username
		command.GitToken = primary.Token
		command.GitCommitName = primary.CommitName
		command.GitCommitEmail = primary.CommitEmail
	}
	return command, gitProfiles, nil
}

func credentialProfileNames(profiles []store.CredentialProfile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.Name)
	}
	return names
}
