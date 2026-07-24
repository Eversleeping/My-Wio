package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestServerCredentialUpdateQueuesEncryptedSecretsAndPersistsAssignmentOnSuccess(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	server := enrollResourceTestServer(t, database, "credential-update-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.7"}); err != nil {
		t.Fatal(err)
	}
	codexCiphertext, _ := api.vault.Encrypt("codex-secret-value")
	gitCiphertext, _ := api.vault.Encrypt("git-token-value")
	codex, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "codex", Name: "Codex", Endpoint: "https://api.example.com/v1", Model: "gpt-5.6-sol"}, codexCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	git, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "git-user", CommitName: "Example User", CommitEmail: "user@example.com"}, gitCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	response := serverCredentialRequest(t, server.ID, map[string]string{"codex_profile_id": codex.ID, "git_profile_id": git.ID}, api.updateServerCredentialProfiles)
	if response.Code != http.StatusAccepted {
		t.Fatalf("credential update returned %d: %s", response.Code, response.Body.String())
	}
	var queued map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(context.Background(), queued["operation_id"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(operation.Payload, "v1:") || strings.Contains(operation.Payload, "codex-secret-value") || strings.Contains(operation.Payload, "git-token-value") {
		t.Fatalf("operation payload was not protected: %q", operation.Payload)
	}
	var command protocol.ConfigureCredentialsCommand
	if err := api.vault.Decrypt(operation.Payload, &command); err != nil {
		t.Fatal(err)
	}
	if command.CodexAPIKey != "codex-secret-value" || command.GitToken != "git-token-value" || command.GitCommitName != git.CommitName || command.GitCommitEmail != git.CommitEmail || command.RemoveGit {
		t.Fatalf("unexpected credential command: %#v", command)
	}
	if err := database.CompleteCredentialUpdate(context.Background(), protocol.OperationResult{OperationID: operation.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.Server(context.Background(), server.ID)
	if err != nil || updated.CodexProfileID != codex.ID || updated.GitProfileID != git.ID || updated.CodexProfileName != codex.Name || updated.GitProfileName != git.Name {
		t.Fatalf("credential assignment was not persisted: %#v %v", updated, err)
	}
}

func TestServerCredentialUpdateRejectsOfflineServerAndWrongProfileKind(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	server := enrollResourceTestServer(t, database, "credential-reject-token")
	ciphertext, _ := api.vault.Encrypt("credential-secret")
	git, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "git-user", CommitName: "Example User", CommitEmail: "user@example.com"}, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	offline := serverCredentialRequest(t, server.ID, map[string]string{"codex_profile_id": git.ID}, api.updateServerCredentialProfiles)
	if offline.Code != http.StatusConflict {
		t.Fatalf("offline server returned %d: %s", offline.Code, offline.Body.String())
	}
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1"}); err != nil {
		t.Fatal(err)
	}
	wrongKind := serverCredentialRequest(t, server.ID, map[string]string{"codex_profile_id": git.ID}, api.updateServerCredentialProfiles)
	if wrongKind.Code != http.StatusBadRequest {
		t.Fatalf("wrong profile kind returned %d: %s", wrongKind.Code, wrongKind.Body.String())
	}
}

func TestServerCredentialUpdateBindsMultipleGitProfiles(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	server := enrollResourceTestServer(t, database, "multiple-credential-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1"}); err != nil {
		t.Fatal(err)
	}
	codexSecret, _ := api.vault.Encrypt("codex-secret-value")
	codex, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "codex", Name: "Codex", Endpoint: "https://api.example.com/v1", Model: "gpt-5.6-sol"}, codexSecret)
	if err != nil {
		t.Fatal(err)
	}
	firstSecret, _ := api.vault.Encrypt("gitee-token-value")
	first, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "Gitee", Endpoint: "https://gitee.com", Username: "gitee-user", CommitName: "Gitee User", CommitEmail: "gitee@example.com"}, firstSecret)
	if err != nil {
		t.Fatal(err)
	}
	secondSecret, _ := api.vault.Encrypt("github-token-value")
	second, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "GitHub", Endpoint: "https://github.com", Username: "github-user", CommitName: "GitHub User", CommitEmail: "github@example.com"}, secondSecret)
	if err != nil {
		t.Fatal(err)
	}
	response := serverCredentialRequest(t, server.ID, map[string]any{"codex_profile_id": codex.ID, "git_profile_ids": []string{first.ID, second.ID}}, api.updateServerCredentialProfiles)
	if response.Code != http.StatusAccepted {
		t.Fatalf("credential update returned %d: %s", response.Code, response.Body.String())
	}
	var queued map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(context.Background(), queued["operation_id"])
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.ConfigureCredentialsCommand
	if err := api.vault.Decrypt(operation.Payload, &command); err != nil {
		t.Fatal(err)
	}
	if len(command.GitCredentials) != 2 || command.GitCredentials[0].Username != first.Username || command.GitCredentials[1].Username != second.Username || command.GitUsername != first.Username {
		t.Fatalf("unexpected multiple Git command: %#v", command)
	}
	if err := database.CompleteCredentialUpdate(context.Background(), protocol.OperationResult{OperationID: operation.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.Server(context.Background(), server.ID)
	if err != nil || updated.GitProfileID != first.ID || len(updated.GitProfiles) != 2 || updated.GitProfiles[0].ID != first.ID || updated.GitProfiles[1].ID != second.ID {
		t.Fatalf("multiple Git profile assignment was not persisted: %#v %v", updated, err)
	}
	servers, err := database.ListServers(context.Background())
	if err != nil || len(servers) != 1 || len(servers[0].GitProfiles) != 2 || servers[0].GitProfiles[0].ID != first.ID || servers[0].GitProfiles[1].ID != second.ID {
		t.Fatalf("server list did not expose multiple Git profiles: %#v %v", servers, err)
	}
}

func serverCredentialRequest(t *testing.T, serverID string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", serverID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	return directJSONRequest(t, http.MethodPost, "/api/servers/"+serverID+"/credential-profiles", body, nil, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(requestContext))
	})
}
