package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestMalformedResourceResultCannotCompleteOperation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "dispatch.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	server, err := database.EnsureControlPlaneServer(ctx, "test-server", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	subscription, events := hub.Subscribe()
	t.Cleanup(func() { hub.Unsubscribe(subscription) })
	gateway := New(database, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, kind := range []string{protocol.OpGitWorkspaceInspect, protocol.OpGitWorkspaceWrite, protocol.OpCodexGoalGet, protocol.OpCodexThreadFork} {
		t.Run(kind, func(t *testing.T) {
			operationID, err := database.QueueOperation(ctx, server.ID, kind, map[string]any{}, kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE agent_operations SET payload=? WHERE id=?"), "{", operationID); err != nil {
				t.Fatal(err)
			}
			err = gateway.handleOperationResult(ctx, server.ID, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: json.RawMessage(`{}`)}, false)
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) {
				t.Fatalf("expected malformed request rejection, got %v", err)
			}
			operation, err := database.Operation(ctx, operationID)
			if err != nil {
				t.Fatal(err)
			}
			if operation.Status != "queued" || operation.CompletedAt != nil {
				t.Fatalf("invalid result completed operation: %#v", operation)
			}
			select {
			case event := <-events:
				t.Fatalf("invalid result published event: %#v", event)
			default:
			}
		})
	}
	if err := gateway.handleOperationResult(ctx, server.ID, protocol.OperationResult{OperationID: "missing", Status: "succeeded"}, false); !store.IsNotFound(err) {
		t.Fatalf("missing operation: %v", err)
	}
}
