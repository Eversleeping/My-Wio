package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWorkspaceFileScanTimeoutAndRetry(t *testing.T) {
	assertWorkspaceFileScanTimeoutAndRetry(t, testStore(t))
}

func assertWorkspaceFileScanTimeoutAndRetry(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, database, "scan-timeout", "scan-timeout")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/scan-timeout", Name: "scan-timeout"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var workspace Workspace
	for _, candidate := range workspaces {
		if candidate.ServerID == server.ID {
			workspace = candidate
		}
	}
	command := protocol.WorkspaceFilesCommand{WorkspaceID: workspace.ID, Path: workspace.Path}
	queueScan := func() Operation {
		t.Helper()
		id, err := database.QueueResourceOperation(ctx, server.ID, "workspace.files", command, NewID(), OperationResource{WorkspaceID: workspace.ID}, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.BeginWorkspaceFileScan(ctx, workspace.ID); err != nil {
			t.Fatal(err)
		}
		operation, err := database.Operation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return operation
	}
	assertSnapshot := func(status, path string) {
		t.Helper()
		snapshot, err := database.WorkspaceFileSnapshot(ctx, workspace.ID)
		if err != nil || snapshot.Status != status || !strings.Contains(snapshot.Files, path) {
			t.Fatalf("unexpected snapshot: %#v %v", snapshot, err)
		}
		if status == "failed" && snapshot.Error != workspaceFileScanTimeoutMessage {
			t.Fatalf("missing actionable timeout error: %#v", snapshot)
		}
	}
	oldData, _ := json.Marshal(protocol.WorkspaceFilesResult{Files: []protocol.WorkspaceFile{{Path: "cached.md", Kind: "file"}}})
	if err := database.SaveWorkspaceFiles(ctx, workspace.ID, protocol.WorkspaceFilesResult{Files: []protocol.WorkspaceFile{{Path: "cached.md", Kind: "file"}}}); err != nil {
		t.Fatal(err)
	}
	// Exercise queued, delivered and running operations, including repeated
	// delivery: the execution deadline must be measured from the first start.
	for _, status := range []string{"queued", "delivered", "running"} {
		t.Run(status, func(t *testing.T) {
			operation := queueScan()
			old := time.Now().UTC().Add(-WorkspaceFileScanTimeout - time.Minute)
			if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE agent_operations SET status=?,created_at=?,delivered_at=?,started_at=? WHERE id=?`), status, old, old, old, operation.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE workspace_file_snapshots SET requested_at=? WHERE workspace_id=?`), old, workspace.ID); err != nil {
				t.Fatal(err)
			}
			if err := database.ExpireWorkspaceFileScans(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			expired, err := database.Operation(ctx, operation.ID)
			if err != nil || expired.Status != "failed" || expired.CompletedAt == nil {
				t.Fatalf("stale operation did not terminate: %#v %v", expired, err)
			}
			assertSnapshot("failed", "cached.md")
			if completed, err := database.CompleteWorkspaceFileScan(ctx, operation, workspace.ID, protocol.OperationResult{OperationID: operation.ID, Status: "succeeded", Data: oldData}); err != nil || completed {
				t.Fatalf("late result was not ignored: completed=%v err=%v", completed, err)
			}
			assertSnapshot("failed", "cached.md")
		})
	}
	// No result was returned after a restart, and the operation itself is gone.
	if err := database.BeginWorkspaceFileScan(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * WorkspaceFileScanTimeout)
	if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE workspace_file_snapshots SET requested_at=? WHERE workspace_id=?`), old, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ExpireWorkspaceFileScans(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("failed", "cached.md")
	// A long queue wait must not consume a running scan's execution deadline.
	recent := queueScan()
	if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE agent_operations SET created_at=?,status='running',started_at=? WHERE id=?`), old, time.Now().UTC(), recent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE workspace_file_snapshots SET requested_at=? WHERE workspace_id=?`), old, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ExpireWorkspaceFileScans(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("scanning", "cached.md")
	if _, err := database.CompleteWorkspaceFileScan(ctx, recent, workspace.ID, protocol.OperationResult{OperationID: recent.ID, Status: "succeeded", Data: oldData}); err != nil {
		t.Fatal(err)
	}
	// A retry is not expired by the old request, nor overwritten by an older
	// result that arrives after the retry has already finished.
	older := queueScan()
	retry := queueScan()
	data, _ := json.Marshal(protocol.WorkspaceFilesResult{Files: []protocol.WorkspaceFile{{Path: "fresh.md", Kind: "file"}}})
	if err := database.ExpireWorkspaceFileScans(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("scanning", "cached.md")
	if _, err := database.CompleteWorkspaceFileScan(ctx, retry, workspace.ID, protocol.OperationResult{OperationID: retry.ID, Status: "succeeded", Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteWorkspaceFileScan(ctx, older, workspace.ID, protocol.OperationResult{OperationID: older.ID, Status: "succeeded", Data: oldData}); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("succeeded", "fresh.md")
	otherID, err := database.QueueResourceOperation(ctx, server.ID, "codex.turn.start", map[string]string{}, NewID(), OperationResource{WorkspaceID: workspace.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE agent_operations SET created_at=?,status='running',started_at=? WHERE id=?`), old, old, otherID); err != nil {
		t.Fatal(err)
	}
	if err := database.ExpireWorkspaceFileScans(ctx, time.Now().Add(2*WorkspaceFileScanTimeout)); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("succeeded", "fresh.md")
	other, err := database.Operation(ctx, otherID)
	if err != nil || other.Status != "running" {
		t.Fatalf("file scan timeout changed a Codex turn: %#v %v", other, err)
	}
}
