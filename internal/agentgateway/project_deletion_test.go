package agentgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestProjectDeletionOperationResultFinalizesMetadataAndPublishes(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "delete-gateway", []string{"/srv"}, "delete-gateway-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "delete-gateway-enrollment")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "delete-gateway.local", "delete-gateway-agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "delete-gateway.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "delete-gateway", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := store.NewID()
	path := "/srv/delete-gateway"
	if _, err := database.DB.ExecContext(ctx, database.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,branch,commit_sha,dirty) VALUES(?,?,?,?,?,'managed','ready','primary','main','abc',0)`), workspaceID, project.ID, server.ID, path, project.Name); err != nil {
		t.Fatal(err)
	}
	operationIDs, deleted, err := database.QueueProjectManagedDeletion(ctx, project.ID)
	if err != nil || deleted || len(operationIDs) != 1 {
		t.Fatalf("queue deletion: %#v %v %v", operationIDs, deleted, err)
	}
	resultData, _ := json.Marshal(protocol.GitProjectDeleteResult{ProjectID: project.ID, WorkspaceID: workspaceID, Path: path, Removed: true})
	resultPayload, _ := json.Marshal(protocol.OperationResult{OperationID: operationIDs[0], Status: "succeeded", Data: resultData})
	hub := realtime.New()
	subscriptionID, events := hub.Subscribe()
	t.Cleanup(func() { hub.Unsubscribe(subscriptionID) })
	gateway := New(database, hub, security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: resultPayload}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Project(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project metadata remains: %v", err)
	}
	operation, err := database.Operation(ctx, operationIDs[0])
	if err != nil || operation.Status != "succeeded" || operation.ProjectID != "" || operation.WorkspaceID != "" {
		t.Fatalf("operation result was not retained after cascade: %#v %v", operation, err)
	}
	select {
	case event := <-events:
		if event.Kind != "operation.succeeded" || event.StreamID != server.ID {
			t.Fatalf("unexpected project deletion event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for project deletion event")
	}
}
