package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
)

func TestCredentialProfilesEncryptSecretsAndReturnMetadataOnly(t *testing.T) {
	database := openBootstrapTestStore(t)
	vault := security.DevVault()
	api := &API{store: database, vault: vault}
	session := &store.Session{UserID: "test-user"}
	created := directJSONRequest(t, http.MethodPost, "/api/credential-profiles", map[string]any{
		"kind": "codex", "name": "Primary Codex", "endpoint": "https://api.example.com/v1", "model": "gpt-5.6-sol", "secret": "codex-secret-value",
	}, session, api.saveCredentialProfile)
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), "codex-secret-value") {
		t.Fatalf("unexpected create response: %d %s", created.Code, created.Body.String())
	}
	var profile store.CredentialProfile
	if err := json.Unmarshal(created.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	stored, err := database.CredentialProfile(context.Background(), profile.ID)
	if err != nil || stored.Ciphertext == "" || strings.Contains(stored.Ciphertext, "codex-secret-value") {
		t.Fatalf("credential was not encrypted: %#v %v", stored, err)
	}
	var secret string
	if err := vault.Decrypt(stored.Ciphertext, &secret); err != nil || secret != "codex-secret-value" {
		t.Fatalf("could not decrypt stored credential: %q %v", secret, err)
	}
	updated := directJSONRequest(t, http.MethodPost, "/api/credential-profiles", map[string]any{
		"id": profile.ID, "kind": "codex", "name": "Primary Codex", "endpoint": "https://api.example.com/v2", "model": "gpt-5.6-terra", "secret": "",
	}, session, api.saveCredentialProfile)
	if updated.Code != http.StatusCreated {
		t.Fatalf("unexpected update response: %d %s", updated.Code, updated.Body.String())
	}
	stored, err = database.CredentialProfile(context.Background(), profile.ID)
	if err != nil || vault.Decrypt(stored.Ciphertext, &secret) != nil || secret != "codex-secret-value" || stored.Endpoint != "https://api.example.com/v2" {
		t.Fatalf("credential update did not retain the secret: %#v %q %v", stored, secret, err)
	}
	listed := directJSONRequest(t, http.MethodGet, "/api/credential-profiles", nil, session, api.credentialProfiles)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "ciphertext") || strings.Contains(listed.Body.String(), "codex-secret-value") {
		t.Fatalf("credential list exposed secret material: %d %s", listed.Code, listed.Body.String())
	}
}

func TestCredentialProfileEndpointValidation(t *testing.T) {
	for name, input := range map[string]credentialProfileInput{
		"local Codex HTTP endpoint": {Kind: "codex", Name: "Local", Endpoint: "http://127.0.0.1:8080/v1", Model: "gpt-5.6-sol", Secret: "secret-value"},
		"Git HTTPS endpoint":        {Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "user", CommitName: "Example User", CommitEmail: "user@example.com", Secret: "secret-value"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCredentialProfile(input); err != nil {
				t.Fatal(err)
			}
		})
	}
	insecureGit := credentialProfileInput{Kind: "git", Name: "Git", Endpoint: "http://gitee.com", Username: "user", CommitName: "Example User", CommitEmail: "user@example.com", Secret: "secret-value"}
	if err := validateCredentialProfile(insecureGit); err == nil {
		t.Fatal("insecure Git credential endpoint was accepted")
	}
	missingIdentity := credentialProfileInput{Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "user", Secret: "secret-value"}
	if err := validateCredentialProfile(missingIdentity); err == nil {
		t.Fatal("Git profile without commit identity was accepted")
	}
}

func TestGitCredentialProfilePersistsCommitIdentityWithoutExposingToken(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := &API{store: database, vault: security.DevVault()}
	response := directJSONRequest(t, http.MethodPost, "/api/credential-profiles", map[string]string{
		"kind": "git", "name": "GitHub", "endpoint": "https://github.com", "username": "git-user", "commit_name": "Example User", "commit_email": "user@users.noreply.github.com", "secret": "git-token-value",
	}, &store.Session{UserID: "test-user"}, api.saveCredentialProfile)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "git-token-value") {
		t.Fatalf("unexpected Git profile response: %d %s", response.Code, response.Body.String())
	}
	var profile store.CredentialProfile
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.CommitName != "Example User" || profile.CommitEmail != "user@users.noreply.github.com" {
		t.Fatalf("commit identity was not persisted: %#v", profile)
	}
}

func TestBootstrapResolvesCodexAndGitCredentialProfiles(t *testing.T) {
	database := openBootstrapTestStore(t)
	vault := security.DevVault()
	codexCiphertext, _ := vault.Encrypt("profile-codex-secret")
	gitCiphertext, _ := vault.Encrypt("profile-git-token")
	codex, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "codex", Name: "Codex preset", Endpoint: "https://api.example.com/v1", Model: "gpt-5.6-sol"}, codexCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	git, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "Git preset", Endpoint: "https://github.com", Username: "git-user", CommitName: "Example User", CommitEmail: "user@users.noreply.github.com"}, gitCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeServerBootstrapper{installResult: sshbootstrap.InstallResult{ServerID: "server-id", Hostname: "node-1", Architecture: "amd64"}}
	api := &API{store: database, vault: vault, bootstrapper: fake, publicURL: "https://wio.example.com", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	input := bootstrapInput()
	delete(input, "codex_api_url")
	delete(input, "codex_api_key")
	delete(input, "codex_model")
	input["codex_profile_id"] = codex.ID
	input["git_profile_id"] = git.ID
	response := directJSONRequest(t, http.MethodPost, "/api/servers/ssh/bootstrap", input, &store.Session{UserID: "test-user"}, api.bootstrapServerSSH)
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap returned %d: %s", response.Code, response.Body.String())
	}
	request := fake.installRequest
	if request.CodexAPIURL != codex.Endpoint || request.CodexAPIKey != "profile-codex-secret" || request.CodexModel != codex.Model || request.GitEndpoint != git.Endpoint || request.GitUsername != git.Username || request.GitToken != "profile-git-token" || request.GitCommitName != git.CommitName || request.GitCommitEmail != git.CommitEmail {
		t.Fatalf("profiles were not resolved into install request: %#v", request)
	}
	for _, secret := range []string{"profile-codex-secret", "profile-git-token"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("bootstrap response exposed %q", secret)
		}
	}
}
