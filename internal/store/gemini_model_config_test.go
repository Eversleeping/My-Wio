package store

import (
	"context"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiModelConfigRevision(t *testing.T) {
	assertGeminiModelConfigRevision(t, testStore(t))
}

func assertGeminiModelConfigRevision(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "model-config", "model-config-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/model-config", Name: "model-config"}}}); err != nil {
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
	profile, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "gemini", Name: "Model config", Endpoint: "https://example.test", Model: "old-model"}, "v1:synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(status string) {
		t.Helper()
		op, err := db.QueueGeminiCredentialUpdate(ctx, server.ID, profile.ID, "v1:synthetic-operation")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CompleteGeminiCredentialUpdate(ctx, protocol.OperationResult{OperationID: op, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	apply("succeeded")
	thread, err := db.CreateGeminiThread(ctx, workspaceID, "old-model", "default")
	if err != nil || thread.CredentialRevision == "" || thread.ModelCredentialRevision != thread.CredentialRevision {
		t.Fatalf("missing initial revision: %+v %v", thread, err)
	}
	previous := thread.CredentialRevision
	apply("failed")
	config, err := db.GeminiModelConfiguration(ctx, server.ID)
	if err != nil || config.Revision != previous {
		t.Fatalf("failed apply changed revision: %+v %v", config, err)
	}
	// Reapplying the same preset must invalidate sessions too (key rotation).
	profile.Model = "new-model"
	if _, err := db.SaveCredentialProfile(ctx, profile, "v1:new-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	apply("succeeded")
	thread, err = db.GeminiThread(ctx, thread.ID)
	if err != nil || thread.CredentialRevision == previous || thread.ModelCredentialRevision != previous || thread.Model != "old-model" {
		t.Fatalf("lost saved/current distinction: %+v %v", thread, err)
	}
	config, err = db.GeminiModelConfiguration(ctx, server.ID)
	storedProfile, profileErr := db.CredentialProfile(ctx, profile.ID)
	if err != nil || profileErr != nil || config.Revision != thread.CredentialRevision || config.ProfileRevision != storedProfile.UpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("applied profile snapshot missing: %+v %v %v", config, err, profileErr)
	}
	if _, err := db.QueueGeminiTurn(ctx, thread, "continue", "new-model", "default"); err != nil {
		t.Fatal(err)
	}
	thread, err = db.GeminiThread(ctx, thread.ID)
	if err != nil || thread.ModelCredentialRevision != thread.CredentialRevision || thread.Model != "new-model" {
		t.Fatalf("send did not acknowledge new config: %+v %v", thread, err)
	}
	if err := db.CompleteGeminiTurn(ctx, thread.ID, thread.OperationID, "succeeded"); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiModelConfigMigration(t *testing.T) {
	assertGeminiModelConfigMigration(t, testStore(t))
}

func assertGeminiModelConfigMigration(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "model-config-legacy", "model-config-legacy-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/config-legacy", Name: "config-legacy"}}}); err != nil {
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
	profile, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "gemini", Name: "Legacy config", Endpoint: "https://example.test", Model: "auto"}, "v1:synthetic")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO server_gemini_credential_profiles(server_id,profile_id,updated_at) VALUES(?,?,?)"), server.ID, profile.ID, now); err != nil {
		t.Fatal(err)
	}
	before, err := db.CreateGeminiThread(ctx, workspaceID, "old-model", "default")
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.CreateGeminiThread(ctx, workspaceID, "intentional-custom", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET updated_at=? WHERE id=?"), now.Add(-time.Hour), before.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET updated_at=? WHERE id=?"), now.Add(time.Hour), after.ID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE gemini_threads DROP COLUMN model_credential_revision",
		"ALTER TABLE server_gemini_credential_profiles DROP COLUMN profile_revision",
		"ALTER TABLE server_gemini_credential_updates DROP COLUMN profile_revision",
	} {
		if _, err := db.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if db.driver == "pgx" {
		// Production migrates in Open with a fresh pool. Retire prepared SELECT
		// t.* statements from this test's earlier, deliberately altered schema.
		db.DB.SetMaxIdleConns(0)
		defer db.DB.SetMaxIdleConns(2)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrateGeminiModelConfig(ctx, db.DB, db.driver); err != nil {
			t.Fatal(err)
		}
		before, err = db.GeminiThread(ctx, before.ID)
		if err != nil || before.ModelCredentialRevision != "" || before.CredentialRevision == "" {
			t.Fatalf("legacy stale model not detected: %+v %v", before, err)
		}
		after, err = db.GeminiThread(ctx, after.ID)
		if err != nil || after.ModelCredentialRevision != after.CredentialRevision || after.Model != "intentional-custom" {
			t.Fatalf("legacy custom model lost: %+v %v", after, err)
		}
		// A later rename/update must not make the next migration bless stale config.
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET updated_at=? WHERE id=?"), now.Add(time.Hour), before.ID); err != nil {
			t.Fatal(err)
		}
	}
}
