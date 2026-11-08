package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func runningTestOperation(t *testing.T, database *store.Store, serverID, kind, key string, age time.Duration) string {
	t.Helper()
	ctx := context.Background()
	id, err := database.QueueOperation(ctx, serverID, kind, map[string]any{}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE agent_operations SET started_at=?,delivered_at=? WHERE id=?"), time.Now().UTC().Add(-age), time.Now().UTC().Add(-age), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func reconcileAnswer(t *testing.T, gateway *Gateway, serverID, operationID, state string) {
	t.Helper()
	payload, _ := json.Marshal(protocol.OperationReconcile{OperationID: operationID, State: state})
	if err := gateway.handle(context.Background(), serverID, &protocol.AgentEnvelope{Kind: protocol.AgentKindOperationReconcile, PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
}

func TestKeepaliveRequestsReconcileOfOverdueRunningOperations(t *testing.T) {
	database, server := gatewayTestServer(t, "reconcile-keepalive-token")
	overdue := runningTestOperation(t, database, server.ID, "deploy.start", "reconcile-overdue", 5*time.Minute)
	runningTestOperation(t, database, server.ID, "git.workspace.write", "reconcile-recent", time.Second)
	stream := &keepaliveStream{ctx: context.Background(), sent: make(chan *protocol.ControlEnvelope, 1)}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.sendKeepalive(stream, server.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	message := <-stream.sent
	var keepalive protocol.GeminiKeepalive
	if err := json.Unmarshal(message.PayloadJSON, &keepalive); err != nil {
		t.Fatal(err)
	}
	if len(keepalive.ReconcileOperationIDs) != 1 || keepalive.ReconcileOperationIDs[0] != overdue {
		t.Fatalf("unexpected reconcile request: %#v", keepalive)
	}
}

func TestReconcileFailsOnlyOperationsTheAgentExplicitlyLost(t *testing.T) {
	database, server := gatewayTestServer(t, "reconcile-answer-token")
	ctx := context.Background()
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	// A long deployment the Agent still executes stays running.
	active := runningTestOperation(t, database, server.ID, "deploy.start", "reconcile-active", 50*time.Minute)
	reconcileAnswer(t, gateway, server.ID, active, protocol.OperationReconcileActive)
	if operation, _ := database.Operation(ctx, active); operation.Status != "running" {
		t.Fatalf("active operation was changed: %#v", operation)
	}

	lost := runningTestOperation(t, database, server.ID, "cli.install", "reconcile-lost", 5*time.Minute)
	reconcileAnswer(t, gateway, server.ID, lost, protocol.OperationReconcileUnknown)
	operation, err := database.Operation(ctx, lost)
	if err != nil || operation.Status != "failed" || operation.Result != protocol.OperationLostMessage {
		t.Fatalf("lost operation was not failed: %#v %v", operation, err)
	}
	blockers, err := database.CLIInstallBlockers(ctx, server.ID, "codex")
	if err != nil || blockers.Busy() {
		t.Fatalf("lost install still blocks maintenance: %#v %v", blockers, err)
	}

	// Gemini and Claude turns keep their ownership protocol, and a delivered
	// row is re-sent rather than failed.
	claude := runningTestOperation(t, database, server.ID, "claude.turn.start", "reconcile-claude", 5*time.Minute)
	reconcileAnswer(t, gateway, server.ID, claude, protocol.OperationReconcileUnknown)
	if operation, _ := database.Operation(ctx, claude); operation.Status != "running" {
		t.Fatalf("Claude turn bypassed its ownership protocol: %#v", operation)
	}
	delivered, err := database.QueueOperation(ctx, server.ID, "workspace.files", map[string]any{}, "reconcile-delivered")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkDelivered(ctx, delivered); err != nil {
		t.Fatal(err)
	}
	reconcileAnswer(t, gateway, server.ID, delivered, protocol.OperationReconcileUnknown)
	if operation, _ := database.Operation(ctx, delivered); operation.Status != "delivered" {
		t.Fatalf("delivered operation was failed: %#v", operation)
	}

	// Another server cannot fail this server's operations.
	payload, _ := json.Marshal(protocol.OperationReconcile{OperationID: active, State: protocol.OperationReconcileUnknown})
	if err := gateway.handle(ctx, "other-server", &protocol.AgentEnvelope{Kind: protocol.AgentKindOperationReconcile, PayloadJSON: payload}); err == nil {
		t.Fatal("cross-server reconcile answer was accepted")
	}
}

func TestReconcileMarksLostWorkspaceLifecyclePartial(t *testing.T) {
	database, server := gatewayTestServer(t, "reconcile-lifecycle-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/source", Name: "source", Branch: "main"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	workspace.ManagementMode = "managed"
	command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: workspace.ID, ProjectID: workspace.ProjectID, Action: "move", SourcePath: workspace.Path, TargetPath: "/srv/managed/moved"}
	operationID, err := database.QueueWorkspaceLifecycle(ctx, workspace, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	reconcileAnswer(t, gateway, server.ID, operationID, protocol.OperationReconcileUnknown)
	updated, err := database.Workspace(ctx, workspace.ID)
	if err != nil || updated.Status != "partial" {
		t.Fatalf("a move of unknown outcome was reported clean: %#v %v", updated, err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "failed" || operation.Result != protocol.OperationLostMessage {
		t.Fatalf("lost lifecycle operation was not failed: %#v %v", operation, err)
	}
}

func TestReconcileFailsDeploymentOfLostDeployOperation(t *testing.T) {
	database, server := gatewayTestServer(t, "reconcile-deploy-token")
	ctx := context.Background()
	project, err := database.CreateProject(ctx, "reconcile-deploy", "https://example.com/reconcile-deploy.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := database.CreateDeployment(ctx, target.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	vault := security.DevVault()
	ciphertext, err := vault.Encrypt(protocol.DeployCommand{DeploymentID: deployment.ID, TargetID: target.ID})
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueEncryptedResourceOperation(ctx, server.ID, "deploy.start", ciphertext, "deploy:"+deployment.ID, store.OperationResource{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveDeploymentStatus(ctx, protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), vault, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reconcileAnswer(t, gateway, server.ID, operationID, protocol.OperationReconcileUnknown)
	updated, err := database.Deployment(ctx, deployment.ID)
	if err != nil || updated.Status != "failed" {
		t.Fatalf("deployment of a lost operation stayed active: %#v %v", updated, err)
	}
	if operation, _ := database.Operation(ctx, operationID); operation.Status != "failed" {
		t.Fatalf("lost deploy operation was not failed: %#v", operation)
	}
}
