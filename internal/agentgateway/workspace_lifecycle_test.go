package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestGatewayCommitsWorkspaceMoveAndRejectsMismatchedResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		resultPath string
		wantStatus string
		wantPath   string
		opStatus   string
	}{
		{name: "success", resultPath: "/srv/managed/moved", wantStatus: "ready", wantPath: "/srv/managed/moved", opStatus: "succeeded"},
		{name: "mismatch", resultPath: "/srv/managed/wrong", wantStatus: "partial", wantPath: "/srv/managed/source", opStatus: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			ctx := context.Background()
			if _, err := database.CreateEnrollment(ctx, "lifecycle", []string{"/srv"}, "lifecycle-token", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			enrollment, _ := database.ConsumeEnrollment(ctx, "lifecycle-token")
			server, err := database.EnrollServer(ctx, enrollment, "lifecycle.local", "lifecycle-agent-token")
			if err != nil {
				t.Fatal(err)
			}
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
			lifecycleResult := protocol.GitWorkspaceLifecycleResult{WorkspaceID: workspace.ID, Action: "move", SourcePath: workspace.Path, TargetPath: test.resultPath}
			data, _ := json.Marshal(lifecycleResult)
			payload, _ := json.Marshal(protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data})
			gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
				t.Fatal(err)
			}
			workspace, err = database.Workspace(ctx, workspace.ID)
			if err != nil || workspace.Status != test.wantStatus || workspace.Path != test.wantPath {
				t.Fatalf("unexpected workspace: %#v %v", workspace, err)
			}
			operation, err := database.Operation(ctx, operationID)
			if err != nil || operation.Status != test.opStatus {
				t.Fatalf("unexpected operation: %#v %v", operation, err)
			}
			if test.opStatus == "failed" && !strings.Contains(operation.Result, "does not match") {
				t.Fatalf("mismatch was not explained: %#v", operation)
			}
		})
	}
}

func TestGatewayCommitsCrossServerWorkspaceClone(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	createServer := func(name, token string) store.Server {
		t.Helper()
		if _, err := database.CreateEnrollment(ctx, name, []string{"/srv"}, token, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		enrollment, err := database.ConsumeEnrollment(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		server, err := database.EnrollServer(ctx, enrollment, name+".local", token+"-agent")
		if err != nil {
			t.Fatal(err)
		}
		return server
	}
	sourceServer := createServer("clone-source", "clone-source-token")
	targetServer := createServer("clone-target", "clone-target-token")
	if err := database.UpsertInventory(ctx, sourceServer.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/source", Name: "source", Branch: "main", CommitSHA: "0123456789012345678901234567890123456789"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	source := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), source.ID); err != nil {
		t.Fatal(err)
	}
	source.ManagementMode = "managed"
	command := protocol.GitWorkspaceCloneCommand{WorkspaceID: store.NewID(), ProjectID: source.ProjectID, Name: "source-copy", Destination: "/srv/managed/copy", RemoteURL: "https://example.com/team/source.git", Branch: "main", ExpectedHead: "0123456789012345678901234567890123456789"}
	operationID, err := database.QueueCrossServerWorkspaceClone(ctx, source, targetServer.ID, command)
	if err != nil {
		t.Fatal(err)
	}
	cloned := protocol.GitWorkspaceCloneResult{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: command.Destination, Branch: command.Branch, CommitSHA: command.ExpectedHead}
	data, _ := json.Marshal(cloned)
	payload, _ := json.Marshal(protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data})
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, targetServer.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	target, err := database.Workspace(ctx, command.WorkspaceID)
	if err != nil || target.ServerID != targetServer.ID || target.ProjectID != source.ProjectID || target.ManagementMode != "managed" || target.CommitSHA != command.ExpectedHead {
		t.Fatalf("unexpected cloned workspace: %#v %v", target, err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "succeeded" {
		t.Fatalf("unexpected clone operation: %#v %v", operation, err)
	}
}
