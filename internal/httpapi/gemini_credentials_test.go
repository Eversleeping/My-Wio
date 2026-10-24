package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiProfilePreservesSecretAndMasksResponses(t *testing.T) {
	db := openBootstrapTestStore(t)
	a := resourceTestAPI(db)
	session := &store.Session{UserID: "test-user"}
	input := credentialProfileInput{Kind: "gemini", Name: "Gemini", Endpoint: "https://generativelanguage.googleapis.com", Model: "flash", Secret: "synthetic-gemini-secret"}
	response := directJSONRequest(t, http.MethodPost, "/api/credential-profiles", input, session, a.saveCredentialProfile)
	if response.Code != 201 || strings.Contains(response.Body.String(), input.Secret) {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var profile store.CredentialProfile
	_ = json.Unmarshal(response.Body.Bytes(), &profile)
	input.ID, input.Secret, input.Model = profile.ID, "", "pro"
	response = directJSONRequest(t, http.MethodPost, "/api/credential-profiles", input, session, a.saveCredentialProfile)
	if response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	stored, err := db.CredentialProfile(context.Background(), profile.ID)
	var secret string
	if err != nil || a.vault.Decrypt(stored.Ciphertext, &secret) != nil || secret != "synthetic-gemini-secret" || stored.Model != "pro" {
		t.Fatal("edit did not retain encrypted secret")
	}
	response = directJSONRequest(t, http.MethodGet, "/api/credential-profiles", nil, session, a.credentialProfiles)
	if response.Code != 200 || strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "ciphertext") {
		t.Fatal("list exposed credentials")
	}
	for _, endpoint := range []string{"https://user:pass@example.com", "https://example.com?key=secret", "file:///tmp/secret"} {
		input.Endpoint = endpoint
		if validateCredentialProfile(input) == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}

func TestGeminiCredentialDispatchCommitsOnlySuccessAndPreservesOtherProfiles(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	a := resourceTestAPI(db)
	server := enrollResourceTestServer(t, db, "gemini-credentials-test")
	secret, _ := a.vault.Encrypt("synthetic-gemini-key")
	profile, err := db.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "gemini", Name: "Gemini", Endpoint: "https://generativelanguage.googleapis.com", Model: "flash"}, secret)
	if err != nil {
		t.Fatal(err)
	}
	request := func() int {
		return serverCredentialRequest(t, server.ID, map[string]string{"profile_id": profile.ID}, a.updateServerGeminiCredentialProfile).Code
	}
	if request() != 409 {
		t.Fatal("offline server accepted credentials")
	}
	_ = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.62"})
	if request() != 409 {
		t.Fatal("old Agent accepted credentials")
	}
	_ = db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.63"})
	codex, _ := db.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "codex", Name: "Other Codex"}, secret)
	_ = db.SetServerCredentialProfiles(ctx, server.ID, codex.ID, "")
	if wrong := serverCredentialRequest(t, server.ID, map[string]string{"profile_id": codex.ID}, a.updateServerGeminiCredentialProfile); wrong.Code != 400 {
		t.Fatal("wrong kind accepted")
	}
	for _, status := range []string{"failed", "succeeded"} {
		response := serverCredentialRequest(t, server.ID, map[string]string{"profile_id": profile.ID}, a.updateServerGeminiCredentialProfile)
		if response.Code != 202 {
			t.Fatalf("queue: %d %s", response.Code, response.Body.String())
		}
		if request() != 409 {
			t.Fatal("duplicate accepted")
		}
		var queued map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &queued)
		op, _ := db.Operation(ctx, queued["operation_id"])
		if !strings.HasPrefix(op.Payload, "v1:") || strings.Contains(op.Payload, "synthetic-gemini-key") {
			t.Fatal("unprotected operation")
		}
		var command protocol.ConfigureGeminiCredentialsCommand
		if a.vault.Decrypt(op.Payload, &command) != nil || command.APIKey != "synthetic-gemini-key" || command.Model != "flash" {
			t.Fatal("incorrect command")
		}
		if err := db.CompleteGeminiCredentialUpdate(ctx, protocol.OperationResult{OperationID: op.ID, Status: status}); err != nil {
			t.Fatal(err)
		}
		updated, err := db.Server(ctx, server.ID)
		if err != nil || updated.CodexProfileID != codex.ID {
			t.Fatal("Codex binding changed")
		}
		if status == "failed" && updated.GeminiProfileID != "" || status == "succeeded" && updated.GeminiProfileID != profile.ID {
			t.Fatal("incorrect Gemini binding")
		}
	}
	// A turn queued elsewhere must prevent a credential change as well.
	id, _ := db.QueueOperation(ctx, server.ID, "gemini.turn.start", map[string]string{}, "busy-gemini")
	if request() != 409 {
		t.Fatal("active turn accepted credential changes")
	}
	_ = db.CompleteOperation(ctx, protocol.OperationResult{OperationID: id, Status: "succeeded"})
}
