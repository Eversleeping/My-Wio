package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestModelsOperationResultUpdatesWorkspaceSnapshot(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "models-node", []string{"/srv"}, "enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "models-node.local", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, test := range []struct {
		name     string
		result   protocol.OperationResult
		expected string
	}{
		{name: "success", result: protocol.OperationResult{Status: "succeeded", Data: json.RawMessage(`{"supported":true,"data":[{"model":"new-model"}],"codex_version":"0.159.3"}`)}, expected: "succeeded"},
		{name: "unsupported", result: protocol.OperationResult{Status: "succeeded", Data: json.RawMessage(`{"supported":false,"reason":"model/list unsupported"}`)}, expected: "succeeded"},
		{name: "failure", result: protocol.OperationResult{Status: "failed", Message: "CLI unavailable"}, expected: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := protocol.CodexSnapshotCommand{ScopeType: "workspace", ScopeID: "workspace-1", Workspace: "/srv/project"}
			operationID, err := database.QueueOperation(ctx, server.ID, "codex.models.list", command, store.NewID())
			if err != nil {
				t.Fatal(err)
			}
			if err := database.BeginCodexSnapshot(ctx, "workspace", command.ScopeID, "models.list"); err != nil {
				t.Fatal(err)
			}
			result := test.result
			result.OperationID = operationID
			payload, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := database.CodexSnapshot(ctx, "workspace", command.ScopeID, "models.list")
			if err != nil || snapshot.Status != test.expected {
				t.Fatalf("unexpected snapshot: %+v %v", snapshot, err)
			}
			if test.name == "success" && snapshot.Data != `[{"model":"new-model"}]` {
				t.Fatalf("missing models: %s", snapshot.Data)
			}
			if test.name == "unsupported" && (snapshot.Supported != 0 || snapshot.Reason != "model/list unsupported") {
				t.Fatalf("missing capability status: %+v", snapshot)
			}
		})
	}
}
