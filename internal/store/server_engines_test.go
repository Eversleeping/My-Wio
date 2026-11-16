package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestHeartbeatPreservesAppliedEngineConfiguration(t *testing.T) {
	assertHeartbeatPreservesAppliedEngineConfiguration(t, testStore(t))
}

func assertHeartbeatPreservesAppliedEngineConfiguration(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "engine-heartbeat", "engine-heartbeat-token")
	if _, err := db.GeminiModelConfiguration(ctx, server.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected initial Gemini binding: %v", err)
	}
	if _, err := db.ClaudeModelConfiguration(ctx, server.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected initial Claude binding: %v", err)
	}
	gemini, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "gemini", Name: "Heartbeat Gemini"}, "v1:synthetic")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "claude", Name: "Heartbeat Claude", Model: "applied-model"}, "v1:synthetic")
	if err != nil {
		t.Fatal(err)
	}
	gop, err := db.QueueGeminiCredentialUpdate(ctx, server.ID, gemini.ID, "v1:gemini-synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteGeminiCredentialUpdate(ctx, protocol.OperationResult{OperationID: gop, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	cop, err := db.QueueClaudeCredentialUpdate(ctx, server.ID, claude.ID, "v1:claude-synthetic", claude.Model)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: cop, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	beforeGemini, err := db.GeminiModelConfiguration(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeClaude, err := db.ClaudeModelConfiguration(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ready := range []bool{true, false} {
		if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.3.26", Capabilities: testCapabilities("gemini-new", "claude-new"), CodexVersion: "codex-new", CodexReady: ready, GeminiVersion: "gemini-new", GeminiReady: ready, ClaudeVersion: "claude-new", ClaudeReady: ready}); err != nil {
			t.Fatal(err)
		}
		afterGemini, err := db.GeminiModelConfiguration(ctx, server.ID)
		if err != nil || afterGemini != beforeGemini {
			t.Fatalf("heartbeat changed Gemini snapshot: %+v -> %+v (%v)", beforeGemini, afterGemini, err)
		}
		afterClaude, err := db.ClaudeModelConfiguration(ctx, server.ID)
		if err != nil || afterClaude != beforeClaude {
			t.Fatalf("heartbeat changed Claude snapshot: %+v -> %+v (%v)", beforeClaude, afterClaude, err)
		}
		current, err := db.Server(ctx, server.ID)
		if err != nil || current.CodexVersion != "codex-new" || current.GeminiVersion != "gemini-new" || current.ClaudeVersion != "claude-new" || current.CodexReady != boolInt(ready) || current.GeminiReady != boolInt(ready) || current.ClaudeReady != boolInt(ready) || current.ClaudeProfileModel != "applied-model" {
			t.Fatalf("heartbeat did not update CLI state: %+v (%v)", current, err)
		}
	}
}
