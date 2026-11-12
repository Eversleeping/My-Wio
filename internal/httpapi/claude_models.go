package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
)

type claudeModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *API) workspaceClaudeModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	workspace, err := a.store.Workspace(r.Context(), chi.URLParam(r, "workspaceID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "workspace not found")
		return
	}
	if err != nil {
		writeError(w, 500, "could not load workspace")
		return
	}
	config, err := a.store.ClaudeModelConfiguration(r.Context(), workspace.ServerID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && config.ProfileID == "" {
		writeJSON(w, 200, map[string]any{"models": []claudeModel{}, "configured": false})
		return
	}
	if err != nil {
		writeError(w, 500, "could not load Claude API configuration")
		return
	}
	profile, err := a.store.CredentialProfile(r.Context(), config.ProfileID)
	var key string
	if err != nil {
		writeStoreError(w, err, "could not load Claude profile")
		return
	}
	if profile.Kind != "claude" || a.vault == nil || a.vault.Decrypt(profile.Ciphertext, &key) != nil || !validCredentialSecret(key) {
		writeError(w, 409, "Claude API profile is unavailable; reapply the server credentials")
		return
	}
	if (config.ProfileRevision != "" && config.ProfileRevision != profile.UpdatedAt.UTC().Format(time.RFC3339Nano)) || (config.ProfileRevision == "" && profile.UpdatedAt.After(config.AppliedAt)) {
		payload, err := a.store.AppliedClaudeCredentialPayload(r.Context(), workspace.ServerID, config)
		var applied protocol.ConfigureClaudeCredentialsCommand
		if err != nil || a.vault.Decrypt(payload, &applied) != nil || applied.Endpoint != profile.Endpoint || applied.APIKey != key {
			writeError(w, 409, "Claude API profile has changed; reapply the server credentials before refreshing models")
			return
		}
	}
	models, err := fetchClaudeModels(r.Context(), profile.Endpoint, key)
	if err != nil {
		// Transport errors and response bodies may contain secrets. Only use
		// the fixed diagnostics returned by fetchClaudeModels.
		writeError(w, 502, err.Error())
		return
	}
	latest, err := a.store.ClaudeModelConfiguration(r.Context(), workspace.ServerID)
	currentProfile, profileErr := a.store.CredentialProfile(r.Context(), config.ProfileID)
	if err != nil {
		writeStoreError(w, err, "could not load Claude configuration")
		return
	}
	if profileErr != nil {
		writeStoreError(w, profileErr, "could not load Claude profile")
		return
	}
	if latest.ProfileID != config.ProfileID || latest.ProfileRevision != config.ProfileRevision || !latest.AppliedAt.Equal(config.AppliedAt) || !currentProfile.UpdatedAt.Equal(profile.UpdatedAt) {
		writeError(w, 409, "Claude API configuration changed; reopen the session to retry")
		return
	}
	writeJSON(w, 200, map[string]any{"models": models, "configured": true, "credential_revision": config.Revision, "default_model": profile.Model})
}

// Uses Anthropic's Models API, including its cursor pagination. No model turn
// is started, and redirect targets never receive the API credential.
func fetchClaudeModels(ctx context.Context, endpoint, key string) ([]claudeModel, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Claude API endpoint")
	}
	// Match Claude Code's base URL semantics: /v1 is appended to the
	// configured base, preserving any proxy path prefix.
	prefix := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/models"
	u.RawPath = prefix + "/v1/models"
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	models := make([]claudeModel, 0)
	seen := make(map[string]bool)
	cursors := make(map[string]bool)
	cursor := ""
	for page := 0; page < 10; page++ {
		query := url.Values{"limit": {"1000"}}
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		u.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("could not create Claude model request")
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("could not reach the Claude model upstream")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("Claude model upstream returned HTTP %d", response.StatusCode)
		}
		var body struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if readErr != nil || len(data) > 2<<20 || json.Unmarshal(data, &body) != nil || body.Data == nil {
			return nil, errors.New("invalid Claude model upstream response")
		}
		for _, item := range body.Data {
			if !validClaudeOptions(item.ID, "default") || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			name := strings.TrimSpace(item.DisplayName)
			if name == "" || len(name) > 256 {
				name = item.ID
			}
			models = append(models, claudeModel{ID: item.ID, Name: name})
		}
		if !body.HasMore {
			return models, nil
		}
		if body.LastID == "" || len(body.LastID) > 256 || cursors[body.LastID] {
			return nil, errors.New("invalid Claude model upstream pagination")
		}
		cursor = body.LastID
		cursors[cursor] = true
	}
	return nil, errors.New("Claude model upstream pagination limit exceeded")
}
