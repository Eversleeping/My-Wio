package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func backdateOperation(t *testing.T, database *Store, id string, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age)
	for _, statement := range []string{
		"UPDATE agent_operations SET created_at=? WHERE id=?",
		"UPDATE agent_operations SET delivered_at=? WHERE id=? AND delivered_at IS NOT NULL",
		"UPDATE agent_operations SET started_at=? WHERE id=? AND started_at IS NOT NULL",
	} {
		if _, err := database.DB.ExecContext(context.Background(), database.Q(statement), at, id); err != nil {
			t.Fatal(err)
		}
	}
}

func pendingIDs(t *testing.T, database *Store, serverID string) map[string]bool {
	t.Helper()
	pending, err := database.PendingOperations(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, operation := range pending {
		ids[operation.ID] = true
	}
	return ids
}

func TestMarkDeliveredRefreshesRedeliveryCutoff(t *testing.T) {
	checkMarkDeliveredRefreshesRedeliveryCutoff(t, testStore(t))
}

// Shared with the PostgreSQL integration run.
func checkMarkDeliveredRefreshesRedeliveryCutoff(t *testing.T, database *Store) {
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, database, "redelivery-server")
	operationID, err := database.QueueOperation(ctx, server.ID, "deploy.start", map[string]any{}, "redelivery")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	if pendingIDs(t, database, server.ID)[operationID] {
		t.Fatal("freshly delivered operation was re-sent")
	}
	backdateOperation(t, database, operationID, time.Minute)
	if !pendingIDs(t, database, server.ID)[operationID] {
		t.Fatal("unacknowledged delivery was not re-sent")
	}
	// Re-sending must restart the 30s cutoff instead of repeating every flush.
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "delivered" {
		t.Fatalf("re-send changed operation state: %#v %v", operation, err)
	}
	if pendingIDs(t, database, server.ID)[operationID] {
		t.Fatal("re-sent operation is re-sent again on the next flush")
	}

	// A running operation is not re-sent, so its timestamps must not move.
	if err := database.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	before, _ := database.Operation(ctx, operationID)
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	after, _ := database.Operation(ctx, operationID)
	if after.Status != "running" || !after.StartedAt.Equal(*before.StartedAt) || !after.DeliveredAt.Equal(*before.DeliveredAt) {
		t.Fatalf("running operation timestamps changed: before=%#v after=%#v", before, after)
	}
}

func TestMarkDeliveredBacksOffRunningInterruptRedelivery(t *testing.T) {
	checkMarkDeliveredBacksOffRunningInterruptRedelivery(t, testStore(t))
}

// Shared with the PostgreSQL integration run.
func checkMarkDeliveredBacksOffRunningInterruptRedelivery(t *testing.T, database *Store) {
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, database, "interrupt-redelivery-server")
	operationID, err := database.QueueOperation(ctx, server.ID, "operation.interrupt", map[string]any{}, "interrupt-redelivery")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	backdateOperation(t, database, operationID, time.Minute)
	if !pendingIDs(t, database, server.ID)[operationID] {
		t.Fatal("stale running interruption was not re-sent")
	}
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	if pendingIDs(t, database, server.ID)[operationID] {
		t.Fatal("running interruption is re-sent on every flush")
	}
}

func TestOverdueRunningOperationsListsOnlyGenericRunningOperations(t *testing.T) {
	checkOverdueRunningOperationsListsOnlyGenericRunningOperations(t, testStore(t))
}

// Shared with the PostgreSQL integration run.
func checkOverdueRunningOperationsListsOnlyGenericRunningOperations(t *testing.T, database *Store) {
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, database, "reconcile-server")
	other := enrollProjectImportTestServer(t, database, "reconcile-other-server")
	queue := func(serverID, kind, key string, running bool, age time.Duration) string {
		t.Helper()
		id, err := database.QueueOperation(ctx, serverID, kind, map[string]any{}, key)
		if err != nil {
			t.Fatal(err)
		}
		if running {
			if err := database.MarkRunning(ctx, id); err != nil {
				t.Fatal(err)
			}
		} else if err := database.MarkDelivered(ctx, id); err != nil {
			t.Fatal(err)
		}
		backdateOperation(t, database, id, age)
		return id
	}
	overdue := queue(server.ID, "git.workspace.write", "overdue", true, 5*time.Minute)
	queue(server.ID, "deploy.start", "recent", true, time.Second)
	queue(server.ID, "workspace.files", "delivered", false, 5*time.Minute)
	queue(server.ID, "claude.turn.start", "claude", true, 5*time.Minute)
	queue(server.ID, "operation.interrupt", "interrupt", true, 5*time.Minute)
	queue(other.ID, "git.workspace.write", "other", true, 5*time.Minute)
	ids, err := database.OverdueRunningOperations(ctx, server.ID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != overdue {
		t.Fatalf("unexpected reconcile candidates: %#v", ids)
	}
	if ReconcilesOperationKind("gemini.turn.start") || !ReconcilesOperationKind("deploy.start") {
		t.Fatal("reconcile kind filter is inconsistent")
	}
}

func TestQueueAgentUpdateRejectsActiveNonIdempotentOperations(t *testing.T) {
	checkQueueAgentUpdateRejectsActiveNonIdempotentOperations(t, testStore(t))
}

// Shared with the PostgreSQL integration run.
func checkQueueAgentUpdateRejectsActiveNonIdempotentOperations(t *testing.T, database *Store) {
	ctx := context.Background()
	server := enrollProjectImportTestServer(t, database, "update-busy-server")
	deploy, err := database.QueueOperation(ctx, server.ID, "deploy.start", map[string]any{}, "update-busy-deploy")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, deploy); err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueOperation(ctx, server.ID, "workspace.files", map[string]any{}, "update-busy-scan"); err != nil {
		t.Fatal(err)
	}
	_, err = database.QueueAgentUpdate(ctx, server.ID, protocol.AgentUpdateCommand{Version: "9.9.9"})
	if !errors.Is(err, ErrAgentUpdateBusy) || !errors.Is(err, ErrAgentUpdateOperationsBusy) || !strings.Contains(err.Error(), "deploy.start") {
		t.Fatalf("active deployment did not block the update: %v", err)
	}
	// Rows older than every Agent deadline are lost results, not live work;
	// they must not block the update an old Agent needs to reconcile them.
	backdateOperation(t, database, deploy, 3*time.Hour)
	if _, err := database.QueueAgentUpdate(ctx, server.ID, protocol.AgentUpdateCommand{Version: "9.9.9"}); err != nil {
		t.Fatalf("restart-safe or stale operations blocked the update: %v", err)
	}
	pending, err := database.AgentUpdateBlockingOperations(ctx, server.ID)
	if err != nil || len(pending) != 1 || pending[0].Kind != "agent.update" {
		t.Fatalf("queued update should block another update: %#v %v", pending, err)
	}
}
