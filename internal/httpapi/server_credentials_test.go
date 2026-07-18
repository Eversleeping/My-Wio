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
	git, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "git-user"}, gitCiphertext)
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
	if command.CodexAPIKey != "codex-secret-value" || command.GitToken != "git-token-value" || command.RemoveGit {
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
	git, err := database.SaveCredentialProfile(context.Background(), store.CredentialProfile{Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "git-user"}, ciphertext)
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
