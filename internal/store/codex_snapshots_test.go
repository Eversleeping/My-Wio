package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCodexSnapshotTimeoutAndRetry(t *testing.T) {
	assertCodexSnapshotTimeoutAndRetry(t, testStore(t))
}

func assertCodexSnapshotTimeoutAndRetry(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, database, "codex-timeout", "codex-timeout")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/codex-timeout", Name: "codex-timeout"}}}); err != nil {
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
	command := protocol.CodexSnapshotCommand{ScopeType: "workspace", ScopeID: workspace.ID, Workspace: workspace.Path}
	old := time.Now().UTC().Add(-2 * CodexSnapshotTimeout)
	queue := func(kind string) Operation {
		t.Helper()
		resource := OperationResource{WorkspaceID: workspace.ID}
		if command.ScopeType == "thread" {
			resource.ThreadID = command.ScopeID
		}
		id, err := database.QueueResourceOperation(ctx, server.ID, kind, command, NewID(), resource, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.BeginCodexSnapshot(ctx, command.ScopeType, command.ScopeID, strings.TrimPrefix(kind, "codex.")); err != nil {
			t.Fatal(err)
		}
		op, err := database.Operation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	age := func(op Operation, status string, started time.Time) Operation {
		t.Helper()
		if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE agent_operations SET status=?,created_at=?,delivered_at=?,started_at=? WHERE id=?`), status, old, old, started, op.ID); err != nil {
			t.Fatal(err)
		}
		op, err := database.Operation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	ageSnapshot := func(kind string) {
		t.Helper()
		if _, err := database.DB.ExecContext(ctx, database.Q(`UPDATE codex_snapshots SET requested_at=? WHERE scope_type=? AND scope_id=? AND kind=?`), old, command.ScopeType, command.ScopeID, kind); err != nil {
			t.Fatal(err)
		}
	}
	assertSnapshot := func(kind, status, data string) {
		t.Helper()
		snapshot, err := database.CodexSnapshot(ctx, command.ScopeType, command.ScopeID, kind)
		if err != nil || snapshot.Status != status || snapshot.Data != data {
			t.Fatalf("unexpected snapshot: %+v %v", snapshot, err)
		}
		if status == "failed" && snapshot.Error != codexSnapshotTimeoutMessage {
			t.Fatalf("missing timeout explanation: %+v", snapshot)
		}
	}
	result := protocol.OperationResult{Status: "succeeded", Data: json.RawMessage(`{"supported":true,"data":[{"model":"fresh"}]}`)}
	for _, kind := range []string{"codex.models.list", "codex.skills.list", "codex.mcp.list", "codex.status.snapshot"} {
		for _, status := range []string{"queued", "delivered", "running"} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				snapshotKind := strings.TrimPrefix(kind, "codex.")
				if err := database.SaveCodexSnapshot(ctx, command.ScopeType, command.ScopeID, snapshotKind, protocol.CodexCapabilityResult{Supported: true, Data: json.RawMessage(`["cached"]`)}); err != nil {
					t.Fatal(err)
				}
				op := age(queue(kind), status, old)
				ageSnapshot(snapshotKind)
				if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
					t.Fatal(err)
				}
				expired, err := database.Operation(ctx, op.ID)
				if err != nil || expired.Status != "failed" || expired.CompletedAt == nil || expired.Result != codexSnapshotTimeoutMessage {
					t.Fatalf("stale operation did not terminate: %+v %v", expired, err)
				}
				assertSnapshot(snapshotKind, "failed", `["cached"]`)
				if completed, err := database.CompleteCodexSnapshotOperation(ctx, op, command, result); err != nil || completed {
					t.Fatalf("late result accepted: %v %v", completed, err)
				}
				assertSnapshot(snapshotKind, "failed", `["cached"]`)
			})
		}
	}
	// An old request can start recently after waiting in the queue.
	recent := age(queue("codex.models.list"), "running", time.Now().UTC())
	ageSnapshot("models.list")
	if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("models.list", "loading", `["cached"]`)
	if completed, err := database.CompleteCodexSnapshotOperation(ctx, recent, command, result); err != nil || !completed {
		t.Fatalf("recently started refresh failed: %v %v", completed, err)
	}
	// A newer loading request survives an older timeout; a completed newer
	// request survives both an older success and an older timeout.
	older := age(queue("codex.models.list"), "running", old)
	retry := queue("codex.models.list")
	ageSnapshot("models.list")
	if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("models.list", "loading", `[{"model":"fresh"}]`)
	if _, err := database.CompleteCodexSnapshotOperation(ctx, retry, command, result); err != nil {
		t.Fatal(err)
	}
	if completed, err := database.CompleteCodexSnapshotOperation(ctx, older, command, result); err != nil || completed {
		t.Fatalf("expired refresh accepted: %v %v", completed, err)
	}
	older = age(queue("codex.models.list"), "running", old.Add(time.Minute))
	retry = queue("codex.models.list")
	if _, err := database.CompleteCodexSnapshotOperation(ctx, retry, command, result); err != nil {
		t.Fatal(err)
	}
	staleResult := protocol.OperationResult{Status: "succeeded", Data: json.RawMessage(`{"supported":true,"data":["stale"]}`)}
	if completed, err := database.CompleteCodexSnapshotOperation(ctx, older, command, staleResult); err != nil || !completed {
		t.Fatalf("older operation did not finish: %v %v", completed, err)
	}
	assertSnapshot("models.list", "succeeded", `[{"model":"fresh"}]`)
	older = age(queue("codex.models.list"), "running", old)
	retry = queue("codex.models.list")
	if _, err := database.CompleteCodexSnapshotOperation(ctx, retry, command, result); err != nil {
		t.Fatal(err)
	}
	if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("models.list", "succeeded", `[{"model":"fresh"}]`)
	// Loading snapshots left without an operation are also recoverable.
	if err := database.BeginCodexSnapshot(ctx, command.ScopeType, command.ScopeID, "models.list"); err != nil {
		t.Fatal(err)
	}
	ageSnapshot("models.list")
	if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("models.list", "failed", `[{"model":"fresh"}]`)
	// Thread information refreshes have their own scope even when the workspace
	// has an active refresh of the same kind.
	thread, err := database.CreateThread(ctx, workspace.ID, "snapshot timeout")
	if err != nil {
		t.Fatal(err)
	}
	queue("codex.status.snapshot")
	command.ScopeType, command.ScopeID = "thread", thread.ID
	threadOld := age(queue("codex.status.snapshot"), "running", old)
	ageSnapshot("status.snapshot")
	if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("status.snapshot", "failed", `{}`)
	if completed, err := database.CompleteCodexSnapshotOperation(ctx, threadOld, command, result); err != nil || completed {
		t.Fatalf("expired thread result accepted: %v %v", completed, err)
	}
	threadOld = age(queue("codex.status.snapshot"), "running", old)
	threadRetry := queue("codex.status.snapshot")
	if _, err := database.CompleteCodexSnapshotOperation(ctx, threadRetry, command, result); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteCodexSnapshotOperation(ctx, threadOld, command, staleResult); err != nil {
		t.Fatal(err)
	}
	assertSnapshot("status.snapshot", "succeeded", `[{"model":"fresh"}]`)
	for _, kind := range []string{"codex.turn.start", "codex.goal.set", "codex.goal.clear", "codex.thread.compact", "deploy.container"} {
		id, err := database.QueueOperation(ctx, server.ID, kind, map[string]string{}, NewID())
		if err != nil {
			t.Fatal(err)
		}
		op, _ := database.Operation(ctx, id)
		age(op, "running", old)
		if err := database.ExpireCodexSnapshots(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		op, err = database.Operation(ctx, id)
		if err != nil || op.Status != "running" {
			t.Fatalf("refresh timeout changed %s: %+v %v", kind, op, err)
		}
	}
}
