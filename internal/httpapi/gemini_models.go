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
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type geminiModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *API) workspaceGeminiModels(w http.ResponseWriter, r *http.Request) {
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
	server, err := a.store.Server(r.Context(), workspace.ServerID)
	if err != nil {
		writeError(w, 404, "server not found")
		return
	}
	if server.GeminiProfileID == "" {
		writeJSON(w, 200, map[string]any{"models": []geminiModel{}, "configured": false})
		return
	}
	profile, err := a.store.CredentialProfile(r.Context(), server.GeminiProfileID)
	var key string
	if err != nil || profile.Kind != "gemini" || a.vault == nil || a.vault.Decrypt(profile.Ciphertext, &key) != nil || !validCredentialSecret(key) {
		writeError(w, 409, "Gemini API profile is unavailable; reapply the server credentials")
		return
	}
	models, err := fetchGeminiModels(r.Context(), profile.Endpoint, key)
	if err != nil {
		// Upstream response bodies and transport errors can contain credentials.
		// Only return the fixed diagnostics produced by fetchGeminiModels.
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": models, "configured": true})
}

func fetchGeminiModels(ctx context.Context, endpoint, key string) ([]geminiModel, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Gemini API endpoint")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/v1beta") && !strings.HasSuffix(u.Path, "/v1") {
		u.Path += "/v1beta"
	}
	u.Path += "/models"
	u.RawPath = ""
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	models := make([]geminiModel, 0)
	seen := make(map[string]bool)
	token := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"pageSize": {"1000"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("could not create Gemini model request")
		}
		req.Header.Set("x-goog-api-key", key)
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("could not reach the Gemini model upstream")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("Gemini model upstream returned HTTP %d", response.StatusCode)
		}
		var body struct {
			Models []struct {
				Name    string   `json:"name"`
				Display string   `json:"displayName"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			Next string `json:"nextPageToken"`
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if readErr != nil || len(data) > 2<<20 || json.Unmarshal(data, &body) != nil {
			return nil, errors.New("invalid Gemini model upstream response")
		}
		for _, item := range body.Models {
			generates := len(item.Methods) == 0
			for _, method := range item.Methods {
				generates = generates || method == "generateContent"
			}
			id := strings.TrimPrefix(item.Name, "models/")
			if !generates || !credentialModelPattern.MatchString(id) || seen[id] {
				continue
			}
			seen[id] = true
			name := strings.TrimSpace(item.Display)
			if name == "" || len(name) > 256 {
				name = id
			}
			models = append(models, geminiModel{ID: id, Name: name})
		}
		if body.Next == "" {
			sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
			return models, nil
		}
		if body.Next == token {
			return nil, errors.New("invalid Gemini model upstream pagination")
		}
		token = body.Next
	}
	return nil, errors.New("Gemini model upstream pagination limit exceeded")
}
