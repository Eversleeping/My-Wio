package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestWorkspaceLifecycleAPIUpdatesNameAndQueuesMove(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-lifecycle-api")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", ManagedRoots: []string{"/srv/managed"}}); err != nil {
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
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPatch, "/api/workspaces/"+workspace.ID, workspace.ID, map[string]string{"display_name": "Primary checkout"}, api.updateWorkspace)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Primary checkout") {
		t.Fatalf("workspace update returned %d: %s", response.Code, response.Body.String())
	}
	response = workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/move", workspace.ID, map[string]string{"path": "/srv/managed/moved"}, api.moveWorkspace)
	if response.Code != http.StatusAccepted {
		t.Fatalf("workspace move returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "git.workspace.lifecycle" || operations[0].WorkspaceWrite != 1 {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.GitWorkspaceLifecycleCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.Action != "move" || command.TargetPath != "/srv/managed/moved" {
		t.Fatalf("unexpected move command: %#v %v", command, err)
	}
}

func TestWorkspaceLifecycleAPIRejectsObservedAndUnrefreshedCrossServerCopy(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-copy-api")
	target := enrollResourceTestServer(t, database, "workspace-copy-target-api")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(ctx, target.ID, protocol.Heartbeat{Hostname: "node-2", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/observed/source", Name: "source"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/move", workspace.ID, map[string]string{"path": "/srv/managed/moved"}, api.moveWorkspace)
	if response.Code != http.StatusConflict {
		t.Fatalf("observed move returned %d: %s", response.Code, response.Body.String())
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	response = workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/copy", workspace.ID, map[string]string{"path": "/srv/managed/copy", "server_id": target.ID}, api.copyWorkspace)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "must be refreshed") {
		t.Fatalf("cross-server copy returned %d: %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceDeletionPlanAndMetadataAPI(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-delete-api")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/observed", Name: "observed"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/deletion-plan", workspace.ID, map[string]any{}, api.workspaceDeletionPlan)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"can_remove_record":true`) || !strings.Contains(response.Body.String(), `"can_delete_files":false`) {
		t.Fatalf("deletion plan returned %d: %s", response.Code, response.Body.String())
	}
	response = workspaceResourceRequest(t, http.MethodDelete, "/api/workspaces/"+workspace.ID+"?mode=metadata", workspace.ID, nil, api.deleteWorkspace)
	if response.Code != http.StatusOK {
		t.Fatalf("metadata delete returned %d: %s", response.Code, response.Body.String())
	}
	if _, err := database.Workspace(ctx, workspace.ID); !store.IsNotFound(err) {
		t.Fatalf("workspace metadata still exists: %v", err)
	}
}

func TestWorkspacePhysicalDeleteQueuesManagedAgentOperation(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-physical-delete-api")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/delete-me", Name: "delete-me"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodDelete, "/api/workspaces/"+workspace.ID+"?mode=files", workspace.ID, nil, api.deleteWorkspace)
	if response.Code != http.StatusAccepted {
		t.Fatalf("physical delete returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "git.workspace.lifecycle" || operations[0].WorkspaceWrite != 1 {
		t.Fatalf("unexpected delete operation: %#v %v", operations, err)
	}
	var command protocol.GitWorkspaceLifecycleCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.Action != "delete" || command.SourcePath != workspace.Path {
		t.Fatalf("unexpected delete command: %#v %v", command, err)
	}
}

func TestCrossServerWorkspaceCopyQueuesVerifiedRemoteClone(t *testing.T) {
	database := openBootstrapTestStore(t)
	sourceServer := enrollResourceTestServer(t, database, "cross-copy-source")
	targetServer := enrollResourceTestServer(t, database, "cross-copy-target")
	ctx := context.Background()
	for _, server := range []store.Server{sourceServer, targetServer} {
		if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: server.Name, ManagedRoots: []string{"/srv/managed"}}); err != nil {
			t.Fatal(err)
		}
	}
	remoteURL := "https://example.com/team/source.git"
	if err := database.UpsertInventory(ctx, sourceServer.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/source", Name: "source", RemoteURL: remoteURL, Branch: "main", CommitSHA: "0123456789012345678901234567890123456789"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := protocol.GitWorkspaceInspectResult{WorkspaceID: workspace.ID, Status: protocol.GitStatus{Branch: "main", Head: "0123456789012345678901234567890123456789", Upstream: "origin/main"}, Remotes: []protocol.GitRemote{{Name: "origin", FetchURLs: []string{remoteURL}, PushURLs: []string{remoteURL}}}}
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspace.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/copy", workspace.ID, map[string]string{"path": "/srv/managed/copied", "server_id": targetServer.ID}, api.copyWorkspace)
	if response.Code != http.StatusAccepted {
		t.Fatalf("cross-server copy returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, targetServer.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "git.workspace.clone" || operations[0].WorkspaceID != workspace.ID {
		t.Fatalf("unexpected target operation: %#v %v", operations, err)
	}
	var command protocol.GitWorkspaceCloneCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.ProjectID != workspace.ProjectID || command.RemoteURL != remoteURL || command.Branch != "main" || command.ExpectedHead != snapshot.Status.Head || command.Destination != "/srv/managed/copied" {
		t.Fatalf("unexpected clone command: %#v %v", command, err)
	}
}

func TestCrossServerWorkspaceCopyBlocksDirtyAndUnpushedCommits(t *testing.T) {
	database := openBootstrapTestStore(t)
	sourceServer := enrollResourceTestServer(t, database, "cross-copy-ahead-source")
	targetServer := enrollResourceTestServer(t, database, "cross-copy-ahead-target")
	ctx := context.Background()
	for _, server := range []store.Server{sourceServer, targetServer} {
		if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: server.Name, ManagedRoots: []string{"/srv/managed"}}); err != nil {
			t.Fatal(err)
		}
	}
	remoteURL := "https://example.com/team/ahead.git"
	if err := database.UpsertInventory(ctx, sourceServer.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/ahead", Name: "ahead", RemoteURL: remoteURL, Branch: "main"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := database.ListWorkspaces(ctx)
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	dirtySnapshot := protocol.GitWorkspaceInspectResult{WorkspaceID: workspace.ID, Status: protocol.GitStatus{Branch: "main", Head: "0123456789012345678901234567890123456789", Upstream: "origin/main", Dirty: true, Untracked: 1}, Remotes: []protocol.GitRemote{{Name: "origin", FetchURLs: []string{remoteURL}}}}
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspace.ID, dirtySnapshot); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/copy", workspace.ID, map[string]string{"path": "/srv/managed/copied", "server_id": targetServer.ID}, api.copyWorkspace)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "must be clean") {
		t.Fatalf("dirty copy returned %d: %s", response.Code, response.Body.String())
	}
	aheadSnapshot := dirtySnapshot
	aheadSnapshot.Status.Dirty = false
	aheadSnapshot.Status.Untracked = 0
	aheadSnapshot.Status.Ahead = 1
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspace.ID, aheadSnapshot); err != nil {
		t.Fatal(err)
	}
	response = workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/copy", workspace.ID, map[string]string{"path": "/srv/managed/copied", "server_id": targetServer.ID}, api.copyWorkspace)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "unpushed local commits") {
		t.Fatalf("unpushed copy returned %d: %s", response.Code, response.Body.String())
	}
}
