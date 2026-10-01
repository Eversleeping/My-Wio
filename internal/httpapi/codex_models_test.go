package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWorkspaceModelsRefreshQueuesAndReadsSnapshot(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "models-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", CodexVersion: "0.159.3", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %+v %v", workspaces, err)
	}
	workspace := workspaces[0]
	api := resourceTestAPI(database)
	path := "/api/workspaces/" + workspace.ID + "/codex/models"
	response := workspaceResourceRequest(t, http.MethodPost, path+"/refresh", workspace.ID, map[string]any{}, api.refreshWorkspaceCodexModels)
	if response.Code != http.StatusAccepted {
		t.Fatalf("refresh returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.models.list" {
		t.Fatalf("unexpected operations: %+v %v", operations, err)
	}
	var command protocol.CodexSnapshotCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.ScopeType != "workspace" || command.ScopeID != workspace.ID || command.Workspace != workspace.Path || command.CodexVersion != "0.159.3" {
		t.Fatalf("unexpected command: %+v", command)
	}
	snapshot, err := database.CodexSnapshot(ctx, "workspace", workspace.ID, "models.list")
	if err != nil || snapshot.Status != "loading" {
		t.Fatalf("unexpected pending snapshot: %+v %v", snapshot, err)
	}
	if err := database.SaveCodexSnapshot(ctx, "workspace", workspace.ID, "models.list", protocol.CodexCapabilityResult{Supported: true, CodexVersion: "0.159.3", Data: json.RawMessage(`[{"model":"new-model","displayName":"New Model"}]`)}); err != nil {
		t.Fatal(err)
	}
	response = workspaceResourceRequest(t, http.MethodGet, path, workspace.ID, nil, api.workspaceCodexModels)
	var body struct {
		Status string           `json:"status"`
		Data   []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Status != "succeeded" || len(body.Data) != 1 || body.Data[0]["model"] != "new-model" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}
