package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestOperationInterruptLifecycle(t *testing.T) {
	assertOperationInterruptLifecycle(t, testStore(t))
}

func assertOperationInterruptLifecycle(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, database, "interrupt-node", "interrupt-node")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.96"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/interrupt", Name: "interrupt"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{}
	for _, candidate := range workspaces {
		if candidate.ServerID == server.ID {
			workspace = candidate
		}
	}
	if workspace.ID == "" {
		t.Fatal("workspace missing")
	}
	queue := func(kind string) Operation {
		t.Helper()
		id, err := database.QueueResourceOperation(ctx, server.ID, kind, map[string]string{"workspace_id": workspace.ID, "path": "README.md"}, NewID(), OperationResource{WorkspaceID: workspace.ID}, false)
		if err != nil {
			t.Fatal(err)
		}
		operation, err := database.Operation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return operation
	}
	operation := queue("git.workspace.inspect")
	first, err := database.RequestOperationInterrupt(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.RequestOperationInterrupt(ctx, operation.ID)
	if err != nil || first != second {
		t.Fatalf("duplicate interruption: %q %q %v", first, second, err)
	}
	stillRunning, _ := database.Operation(ctx, operation.ID)
	if stillRunning.Status != "queued" {
		t.Fatal("request claimed termination before Agent confirmation")
	}
	pending, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(pending) < 2 || pending[0].ID != first {
		t.Fatalf("interrupt not prioritized: %#v %v", pending, err)
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: first, Status: "failed", Message: "termination not confirmed"}); err != nil {
		t.Fatal(err)
	}
	retry, err := database.RequestOperationInterrupt(ctx, operation.ID)
	if err != nil || retry == first {
		t.Fatalf("failed interruption could not retry: %q %v", retry, err)
	}
	if err := database.MarkRunning(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE agent_operations SET started_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), retry); err != nil {
		t.Fatal(err)
	}
	pending, err = database.PendingOperations(ctx, server.ID)
	if err != nil || len(pending) == 0 || pending[0].ID != retry {
		t.Fatalf("interruption lost across Agent restart: %#v %v", pending, err)
	}
	list, err := database.ListOperations(ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range list {
		if row.ID == operation.ID {
			found = row.InterruptSupported && row.InterruptStatus == "running"
		}
	}
	if !found {
		t.Fatal("operation list missing interrupt capability or progress")
	}
	// A third attempt chains after the second. A lookup that misses the
	// second attempt (a text range under a linguistic collation) reuses its
	// idempotency key and fails.
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: retry, Status: "failed", Message: "termination not confirmed again"}); err != nil {
		t.Fatal(err)
	}
	list, err = database.ListOperations(ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list {
		if row.ID == operation.ID && (row.InterruptStatus != "failed" || row.InterruptError != "termination not confirmed again") {
			t.Fatalf("operation list shows a stale interruption: %#v", row)
		}
	}
	third, err := database.RequestOperationInterrupt(ctx, operation.ID)
	if err != nil || third == first || third == retry {
		t.Fatalf("third interruption: %q %v", third, err)
	}
	if again, err := database.RequestOperationInterrupt(ctx, operation.ID); err != nil || again != third {
		t.Fatalf("active third interruption was not reused: %q %v", again, err)
	}
	list, err = database.ListOperations(ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list {
		if row.ID == operation.ID && (row.InterruptStatus != "queued" || row.InterruptError != "") {
			t.Fatalf("operation list missed the newest interruption: %#v", row)
		}
	}

	for _, kind := range []string{"git.workspace.inspect", "workspace.files", "workspace.changes", "workspace.file.preview", "workspace.diff.preview", "inventory.scan"} {
		t.Run(kind, func(t *testing.T) {
			var begin func(context.Context, string) error
			var table string
			switch kind {
			case "git.workspace.inspect":
				begin, table = database.BeginWorkspaceGitRefresh, "workspace_git_snapshots"
			case "workspace.files":
				begin, table = database.BeginWorkspaceFileScan, "workspace_file_snapshots"
			case "workspace.changes":
				begin, table = database.BeginWorkspaceChangeScan, "workspace_change_snapshots"
			case "workspace.file.preview":
				begin, table = func(ctx context.Context, id string) error {
					return database.BeginWorkspaceFilePreview(ctx, id, "README.md")
				}, "workspace_file_previews"
			case "workspace.diff.preview":
				begin, table = func(ctx context.Context, id string) error {
					return database.BeginWorkspaceDiffPreview(ctx, id, "README.md")
				}, "workspace_diff_previews"
			}
			if begin != nil && kind != "workspace.files" {
				if err := begin(ctx, workspace.ID); err != nil {
					t.Fatal(err)
				}
			}
			target := queue(kind)
			// The file scan endpoint begins its snapshot after queuing.
			if kind == "workspace.files" {
				if err := begin(ctx, workspace.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := protocol.OperationResult{OperationID: target.ID, Status: "cancelled", Message: "interrupted by user"}
			if changed, err := database.CompleteInterruptedOperation(ctx, target, result); err != nil || !changed {
				t.Fatalf("completion: %v %v", changed, err)
			}
			if table != "" {
				var status string
				if err := database.DB.GetContext(ctx, &status, database.Q("SELECT status FROM "+table+" WHERE workspace_id=?"), workspace.ID); err != nil || status != "failed" {
					t.Fatalf("snapshot still loading: %q %v", status, err)
				}
				if err := begin(ctx, workspace.ID); err != nil {
					t.Fatal(err)
				}
				if changed, err := database.CompleteInterruptedOperation(ctx, target, result); err != nil || changed {
					t.Fatalf("duplicate completion changed new request: %v %v", changed, err)
				}
				if err := database.DB.GetContext(ctx, &status, database.Q("SELECT status FROM "+table+" WHERE workspace_id=?"), workspace.ID); err != nil || status == "failed" {
					t.Fatalf("new request overwritten: %q %v", status, err)
				}
			}
			if _, err := database.RequestOperationInterrupt(ctx, target.ID); !errors.Is(err, ErrOperationNotActive) {
				t.Fatalf("terminal task accepted interruption: %v", err)
			}
		})
	}

	// A late cancellation must not release a newer Git refresh.
	if err := database.BeginWorkspaceGitRefresh(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	old := queue("git.workspace.inspect")
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspace_git_snapshots SET requested_at=? WHERE workspace_id=?"), time.Now().UTC().Add(time.Second), workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteInterruptedOperation(ctx, old, protocol.OperationResult{OperationID: old.ID, Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.WorkspaceGitSnapshot(ctx, workspace.ID)
	if err != nil || snapshot.Status != "refreshing" {
		t.Fatalf("new refresh overwritten by old interruption: %#v %v", snapshot, err)
	}
	completed := queue("inventory.scan")
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: completed.ID, Status: "succeeded", Message: "already finished"}); err != nil {
		t.Fatal(err)
	}
	if changed, err := database.CompleteInterruptedOperation(ctx, completed, protocol.OperationResult{OperationID: completed.ID, Status: "cancelled"}); err != nil || changed {
		t.Fatalf("finished task overwritten: %v %v", changed, err)
	}
}

func TestListOperationsMatchesInterruptionsByExactTarget(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "exact-interrupt", "exact-interrupt")
	now := time.Now().UTC()
	insert := func(id, kind, key, status string, at time.Time) {
		t.Helper()
		if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO agent_operations(id,server_id,kind,payload,status,idempotency_key,created_at) VALUES(?,?,?,'{}',?,?,?)"), id, server.ID, kind, status, key, at); err != nil {
			t.Fatal(err)
		}
	}
	// Target IDs that share a prefix or contain the key separator.
	insert("op1", "inventory.scan", "scan-op1", "running", now)
	insert("op10", "inventory.scan", "scan-op10", "running", now)
	insert("op1:x", "inventory.scan", "scan-op1:x", "running", now)
	insert("plain", "inventory.scan", "scan-plain", "running", now)
	insert("interrupt-op10", "operation.interrupt", "operation-interrupt:op10:", "failed", now.Add(time.Second))
	insert("interrupt-op1x", "operation.interrupt", "operation-interrupt:op1:x:", "running", now.Add(time.Second))
	insert("interrupt-op1-a", "operation.interrupt", "operation-interrupt:op1:", "failed", now.Add(time.Second))
	insert("interrupt-op1-b", "operation.interrupt", "operation-interrupt:op1:interrupt-op1-a", "queued", now.Add(2*time.Second))
	list, err := database.ListOperations(ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"op1": "queued", "op10": "failed", "op1:x": "running", "plain": ""}
	for _, row := range list {
		if status, ok := want[row.ID]; ok {
			if row.InterruptStatus != status {
				t.Fatalf("%s interruption = %q, want %q", row.ID, row.InterruptStatus, status)
			}
			delete(want, row.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("operations missing from the list: %v", want)
	}
}
