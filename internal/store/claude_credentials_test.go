package store

import (
	"context"
	"errors"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeCredentialBindingsAndReplay(t *testing.T) {
	assertClaudeCredentialBindingsAndReplay(t, testStore(t))
}

func assertClaudeCredentialBindingsAndReplay(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-api-binding", "claude-api-binding")
	first, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "claude", Name: "First Claude", Endpoint: "https://example.test", Model: "flash"}, "v1:synthetic-test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "claude", Name: "Second Claude", Endpoint: "https://example.test", Model: "pro"}, "v1:synthetic-test")
	if err != nil {
		t.Fatal(err)
	}
	old := ""
	for _, profile := range []CredentialProfile{first, second} {
		id, err := db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, "v1:synthetic-operation", profile.Model)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, "v1:synthetic-operation", profile.Model); !errors.Is(err, ErrClaudeCredentialsBusy) {
			t.Fatal("duplicate update was accepted")
		}
		if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: id, Status: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		if old == "" {
			old = id
		}
	}
	if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: old, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	updated, err := db.Server(ctx, server.ID)
	if err != nil || updated.ClaudeProfileID != second.ID || updated.ClaudeProfileName != second.Name {
		t.Fatalf("old result overwrote latest binding: %+v %v", updated, err)
	}
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range servers {
		if item.ID == server.ID {
			found = item.ClaudeProfileID == second.ID
		}
	}
	if !found {
		t.Fatal("list omitted Claude binding")
	}
}

func TestClaudeAppliedModelIsSnapshotAndFailedUpdatesDoNotChangeIt(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	server := createOperationTestServer(t, db, "claude-snapshot", "claude-snapshot")
	profile, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "claude", Name: "Claude", Endpoint: "https://example.test", Model: "sonnet"}, "v1:synthetic")
	if err != nil {
		t.Fatal(err)
	}
	op, err := db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, "v1:protected", profile.Model)
	if err != nil {
		t.Fatal(err)
	}
	profile.Model = "opus"
	if _, err := db.SaveCredentialProfile(ctx, profile, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: op, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Server(ctx, server.ID)
	if err != nil || got.ClaudeProfileModel != "sonnet" {
		t.Fatalf("lost applied model snapshot: %#v %v", got, err)
	}
	op, err = db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, "v1:protected", "opus")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: op, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	got, err = db.Server(ctx, server.ID)
	if err != nil || got.ClaudeProfileModel != "sonnet" {
		t.Fatalf("failed update changed model: %#v %v", got, err)
	}
}

func TestClaudeMaintenanceAndLostProcessBlockEachOther(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	server := createOperationTestServer(t, db, "claude-locks", "claude-locks")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-locks", Name: "locks"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v", err)
	}
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := db.SaveCredentialProfile(ctx, CredentialProfile{Kind: "claude", Name: "Claude Locks", Endpoint: "https://example.test", Model: "sonnet"}, "v1:synthetic")
	if err != nil {
		t.Fatal(err)
	}
	configure := func() (string, error) {
		return db.QueueClaudeCredentialUpdate(ctx, server.ID, profile.ID, "v1:protected", profile.Model)
	}
	op, err := configure()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "test", "sonnet", "default", ""); !errors.Is(err, ErrClaudeCredentialsBusy) {
		t.Fatalf("turn during configuration: %v", err)
	}
	if _, err := db.QueueCLIInstall(ctx, server.ID, protocol.CLIInstallCommand{Kind: "claude", Version: "2.1.318"}); !errors.Is(err, ErrCLIInstallBusy) {
		t.Fatalf("install during configuration: %v", err)
	}
	if err := db.CompleteClaudeCredentialUpdate(ctx, protocol.OperationResult{OperationID: op, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "test", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := configure(); !errors.Is(err, ErrClaudeCredentialsBusy) {
		t.Fatalf("configuration during task: %v", err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='interrupted' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueCLIInstall(ctx, server.ID, protocol.CLIInstallCommand{Kind: "claude", Version: "2.1.318"}); !errors.Is(err, ErrCLIInstallBusy) {
		t.Fatalf("install with uncertain process: %v", err)
	}
	if _, err := db.QueueAgentUpdate(ctx, server.ID, protocol.AgentUpdateCommand{}); !errors.Is(err, ErrAgentUpdateBusy) {
		t.Fatalf("restart with uncertain process: %v", err)
	}
}
