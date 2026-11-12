package store

import (
	"context"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeModelConfigRevision(t *testing.T) {
	assertClaudeModelConfigRevision(t, testStore(t))
}

func assertClaudeModelConfigRevision(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-model-config", "claude-model-config-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-model-config", Name: "models"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := ""
	for _, workspace := range workspaces {
		if workspace.ServerID == server.ID {
			workspaceID = workspace.ID
		}
	}
	profile, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "claude", Name: "Models", Endpoint: "https://example.test", Model: "old-model"}, "v1:synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	op, err := db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, "v1:synthetic-command", profile.Model)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: op, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	config, err := db.ClaudeModelConfiguration(ctx, server.ID)
	if err != nil || config.Revision == "" {
		t.Fatalf("missing revision: %+v %v", config, err)
	}
	thread, err := db.CreateClaudeThread(ctx, workspaceID, "intentional-custom", "default", "high")
	if err != nil || thread.ModelCredentialRevision != config.Revision {
		t.Fatalf("new session did not acknowledge configuration: %+v %v", thread, err)
	}
	profile.Model = "new-model"
	if _, err := db.SaveCredentialProfile(ctx, profile, ""); err != nil {
		t.Fatal(err)
	}
	latest, err := db.ClaudeModelConfiguration(ctx, server.ID)
	if err != nil || latest.Revision == config.Revision || latest.ProfileRevision != config.ProfileRevision {
		t.Fatalf("model change did not invalidate model choice: %+v %v", latest, err)
	}
	if payload, err := db.AppliedClaudeCredentialPayload(ctx, server.ID, latest); err != nil || payload != "v1:synthetic-command" {
		t.Fatalf("lost applied snapshot: %v", err)
	}
	profile.Name = "Renamed Models"
	if _, err := db.SaveCredentialProfile(ctx, profile, ""); err != nil {
		t.Fatal(err)
	}
	renamed, err := db.ClaudeModelConfiguration(ctx, server.ID)
	if err != nil || renamed.Revision != latest.Revision {
		t.Fatalf("unrelated edit invalidated model choice: %+v %v", renamed, err)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "continue", "new-custom", "default", "high"); err != nil {
		t.Fatal(err)
	}
	thread, err = db.ClaudeThread(ctx, thread.ID)
	if err != nil || thread.ModelCredentialRevision != latest.Revision || thread.Model != "new-custom" {
		t.Fatalf("send did not acknowledge configuration: %+v %v", thread, err)
	}

	// Exercise the legacy backfill twice: later metadata updates must not cause
	// a restart to bless a stale choice or reset a newer intentional choice.
	before, err := db.CreateClaudeThread(ctx, workspaceID, "legacy-old", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.CreateClaudeThread(ctx, workspaceID, "legacy-custom", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET updated_at=? WHERE id=?"), time.Now().UTC().Add(-time.Hour), before.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, "ALTER TABLE claude_threads DROP COLUMN model_credential_revision"); err != nil {
		t.Fatal(err)
	}
	if db.driver == "pgx" {
		db.DB.SetMaxIdleConns(0)
		defer db.DB.SetMaxIdleConns(2)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrateClaudeModelConfig(ctx, db.DB, db.driver); err != nil {
			t.Fatal(err)
		}
		before, err = db.ClaudeThread(ctx, before.ID)
		if err != nil || before.ModelCredentialRevision != "" {
			t.Fatalf("stale legacy model was acknowledged: %+v %v", before, err)
		}
		after, err = db.ClaudeThread(ctx, after.ID)
		if err != nil || after.ModelCredentialRevision != latest.Revision || after.Model != "legacy-custom" {
			t.Fatalf("legacy custom choice lost: %+v %v", after, err)
		}
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET updated_at=? WHERE id=?"), time.Now().UTC(), before.ID); err != nil {
			t.Fatal(err)
		}
	}
}
