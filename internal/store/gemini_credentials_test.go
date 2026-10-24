package store

import (
	"context"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiCredentialBindingsAndReplay(t *testing.T) {
	assertGeminiCredentialBindingsAndReplay(t, testStore(t))
}

func assertGeminiCredentialBindingsAndReplay(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "gemini-api-binding", "gemini-api-binding")
	first, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "gemini", Name: "First Gemini", Endpoint: "https://example.test", Model: "flash"}, "v1:synthetic-test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "gemini", Name: "Second Gemini", Endpoint: "https://example.test", Model: "pro"}, "v1:synthetic-test")
	if err != nil {
		t.Fatal(err)
	}
	old := ""
	for _, profile := range []CredentialProfile{first, second} {
		id, err := db.QueueGeminiCredentialUpdate(ctx, server.ID, profile.ID, "v1:synthetic-operation")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.QueueGeminiCredentialUpdate(ctx, server.ID, profile.ID, "v1:synthetic-operation"); !errors.Is(err, ErrGeminiCredentialsBusy) {
			t.Fatal("duplicate update was accepted")
		}
		if err := db.CompleteGeminiCredentialUpdate(ctx, protocol.OperationResult{OperationID: id, Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		if old == "" {
			old = id
		}
	}
	if err := db.CompleteGeminiCredentialUpdate(ctx, protocol.OperationResult{OperationID: old, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	updated, err := db.Server(ctx, server.ID)
	if err != nil || updated.GeminiProfileID != second.ID || updated.GeminiProfileName != second.Name {
		t.Fatalf("old result overwrote latest binding: %+v %v", updated, err)
	}
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range servers {
		if item.ID == server.ID {
			found = item.GeminiProfileID == second.ID
		}
	}
	if !found {
		t.Fatal("list omitted Gemini binding")
	}
}
