package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWorkspaceRemovalSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	databaseURL := filepath.Join(t.TempDir(), "workspace-removal.db") + "?_pragma=foreign_keys(1)"
	database, err := Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if database != nil {
			_ = database.Close()
		}
	})
	server := createOperationTestServer(t, database, "persist-removal", "persist-removal-token")
	inventory := protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/persist-removal", Name: "persist-removal"}}}
	if err := database.UpsertInventory(ctx, server.ID, inventory); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("initial workspaces: %+v %v", workspaces, err)
	}
	if err := database.RemoveWorkspaceRecord(ctx, workspaces[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, inventory); err != nil {
		t.Fatal(err)
	}
	workspaces, err = database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 0 {
		t.Fatalf("restart restored removed workspaces: %+v %v", workspaces, err)
	}
}

func TestWorkspaceInventoryRemovalTransactions(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "atomic-removal", "atomic-removal-token")
	inventory := protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/atomic-removal", Name: "atomic-removal"}}}
	if err := database.UpsertInventory(ctx, server.ID, inventory); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("initial workspaces: %+v %v", workspaces, err)
	}
	if _, err := database.DB.Exec(`CREATE TRIGGER reject_workspace_removal BEFORE DELETE ON workspaces BEGIN SELECT RAISE(ABORT,'test removal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := database.RemoveWorkspaceRecord(ctx, workspaces[0].ID); err == nil {
		t.Fatal("expected removal failure")
	}
	var exclusions int
	if err := database.DB.GetContext(ctx, &exclusions, "SELECT COUNT(*) FROM workspace_inventory_exclusions"); err != nil || exclusions != 0 {
		t.Fatalf("failed removal left an exclusion: count=%d err=%v", exclusions, err)
	}
	if _, err := database.DB.Exec("DROP TRIGGER reject_workspace_removal"); err != nil {
		t.Fatal(err)
	}
	if err := database.RemoveWorkspaceRecord(ctx, workspaces[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(`CREATE TRIGGER reject_rediscovery BEFORE DELETE ON workspace_inventory_exclusions BEGIN SELECT RAISE(ABORT,'test rediscovery failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueProjectDiscovery(ctx, server.ID); err == nil {
		t.Fatal("expected discovery failure")
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 0 {
		t.Fatalf("failed discovery left queued operations: %+v %v", operations, err)
	}
	if err := database.UpsertInventory(ctx, server.ID, inventory); err != nil {
		t.Fatal(err)
	}
	workspaces, err = database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 0 {
		t.Fatalf("failed discovery restored removed workspaces: %+v %v", workspaces, err)
	}
}
