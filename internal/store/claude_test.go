package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeEffortStorage(t *testing.T) { assertClaudeEffortStorage(t, testStore(t)) }

func assertClaudeEffortStorage(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-effort", "claude-effort")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-effort", Name: "effort"}}}); err != nil {
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
	thread, err := db.CreateClaudeThread(ctx, workspaceID, "sonnet", "default", "high")
	if err != nil || thread.Effort != "high" {
		t.Fatalf("create effort: %+v %v", thread, err)
	}
	for _, effort := range []string{"max", ""} {
		if _, err := db.QueueClaudeTurn(ctx, thread, "prompt", "sonnet", "default", effort); err != nil {
			t.Fatal(err)
		}
		thread, err = db.ClaudeThread(ctx, thread.ID)
		if err != nil || thread.Effort != effort {
			t.Fatalf("saved effort: %+v %v", thread, err)
		}
		op, err := db.Operation(ctx, thread.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		var command protocol.ClaudeTurnCommand
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil || command.Effort != effort {
			t.Fatalf("queued effort: %+v %v", command, err)
		}
		if err := db.InterruptClaudeTurn(ctx, thread); err != nil {
			t.Fatal(err)
		}
	}
	// Older controllers select t.* into their original session structure.
	rows, err := db.DB.QueryxContext(ctx, "SELECT * FROM claude_threads LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if column == "effort" {
			t.Fatal("effort must not change legacy SELECT t.*")
		}
	}
}
