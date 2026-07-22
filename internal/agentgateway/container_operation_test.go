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

func TestContainerOperationResultUpdatesDeploymentTargetState(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "container-node", []string{"/srv"}, "container-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "container-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "container-node.local", "container-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "container-project", "https://example.com/container-project.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}
	vault := security.DevVault()
	ciphertext, err := vault.Encrypt(protocol.ContainerActionCommand{TargetID: target.ID, Action: "stop", ReleaseRoot: target.ReleaseRoot, ComposeFile: target.ComposeFile})
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueDeploymentContainerOperation(ctx, target.ID, server.ID, "stop", ciphertext, "gateway-container-stop")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(protocol.ContainerActionResult{TargetID: target.ID, Action: "stop", State: "stopped", Message: "Docker Compose project stopped"})
	if err != nil {
		t.Fatal(err)
	}
	result := protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), vault, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.DeploymentTarget(ctx, target.ID)
	if err != nil || updated.ContainerStatus != "stopped" || updated.ContainerAction != "stop" || updated.ContainerOperationID != operationID {
		t.Fatalf("unexpected container state after gateway result: %#v %v", updated, err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "succeeded" {
		t.Fatalf("container operation was not completed: %#v %v", operation, err)
	}
}
