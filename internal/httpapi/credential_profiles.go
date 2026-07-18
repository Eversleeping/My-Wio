package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/gitidentity"
	"github.com/wio-platform/wio/internal/store"
)

var credentialModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type credentialProfileInput struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	Model       string `json:"model"`
	CommitName  string `json:"commit_name"`
	CommitEmail string `json:"commit_email"`
	Secret      string `json:"secret"`
}

func (a *API) credentialProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := a.store.ListCredentialProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list credential profiles")
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (a *API) saveCredentialProfile(w http.ResponseWriter, r *http.Request) {
	var input credentialProfileInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ID = strings.TrimSpace(input.ID)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Name = strings.TrimSpace(input.Name)
	input.Endpoint = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	input.Username = strings.TrimSpace(input.Username)
	input.Model = strings.TrimSpace(input.Model)
	input.CommitName = strings.TrimSpace(input.CommitName)
	input.CommitEmail = strings.TrimSpace(input.CommitEmail)
	if input.ID != "" {
		existing, err := a.store.CredentialProfile(r.Context(), input.ID)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "credential profile not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load credential profile")
			return
		}
		if existing.Kind != input.Kind {
			writeError(w, http.StatusBadRequest, "credential profile kind cannot be changed")
			return
		}
	}
	if err := validateCredentialProfile(input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ciphertext := ""
	if input.Secret != "" {
		var err error
		ciphertext, err = a.vault.Encrypt(input.Secret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not encrypt credential profile")
			return
		}
	}
	profile, err := a.store.SaveCredentialProfile(r.Context(), store.CredentialProfile{ID: input.ID, Kind: input.Kind, Name: input.Name, Endpoint: input.Endpoint, Username: input.Username, Model: input.Model, CommitName: input.CommitName, CommitEmail: input.CommitEmail}, ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "credential profile not found")
		return
	}
	if databaseConflict(err) {
		writeError(w, http.StatusConflict, "credential profile name already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save credential profile")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "vault.credential_profile.write", "credential_profile", profile.ID, map[string]any{"kind": profile.Kind, "name": profile.Name, "endpoint": profile.Endpoint, "secret_replaced": input.Secret != ""}, clientIP(r))
	writeJSON(w, http.StatusCreated, profile)
}

func validateCredentialProfile(input credentialProfileInput) error {
	if input.Kind != "codex" && input.Kind != "git" {
		return errors.New("credential profile kind must be codex or git")
	}
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 128 {
		return errors.New("credential profile name is required")
	}
	parsed, err := url.Parse(input.Endpoint)
	validScheme := parsed != nil && (parsed.Scheme == "https" || input.Kind == "codex" && parsed.Scheme == "http")
	if err != nil || !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("credential profile endpoint is invalid")
	}
	if input.Kind == "codex" {
		if !credentialModelPattern.MatchString(input.Model) {
			return errors.New("Codex model is invalid")
		}
		if input.Username != "" {
			return errors.New("Codex credential profile must not include a username")
		}
		if input.CommitName != "" || input.CommitEmail != "" {
			return errors.New("Codex credential profile must not include a Git commit identity")
		}
	} else {
		if input.Username == "" || utf8.RuneCountInString(input.Username) > 256 || strings.ContainsAny(input.Username, "\r\n\x00") {
			return errors.New("Git username is required")
		}
		if input.Model != "" {
			return errors.New("Git credential profile must not include a model")
		}
		if _, _, err := gitidentity.Normalize(input.CommitName, input.CommitEmail); err != nil {
			return err
		}
	}
	if input.ID == "" && !validCredentialSecret(input.Secret) {
		return errors.New("credential secret is required")
	}
	if input.Secret != "" && !validCredentialSecret(input.Secret) {
		return errors.New("credential secret is invalid")
	}
	return nil
}

func validCredentialSecret(value string) bool {
	return len(value) >= 8 && len(value) <= 16<<10 && !strings.ContainsAny(value, "\r\n\x00")
}

func (a *API) deleteCredentialProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "profileID")
	profile, err := a.store.CredentialProfile(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "credential profile not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load credential profile")
		return
	}
	if err := a.store.DeleteCredentialProfile(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete credential profile")
		return
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "vault.credential_profile.delete", "credential_profile", id, map[string]string{"kind": profile.Kind, "name": profile.Name}, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
