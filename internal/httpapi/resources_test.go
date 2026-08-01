package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestUpdateServerMetadata(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "node-1", []string{"/srv"}, "update-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "update-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "node-1.local", "update-agent-token")
	if err != nil {
		t.Fatal(err)
	}

	api := &API{store: database}
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", server.ID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	response := directJSONRequest(t, http.MethodPatch, "/api/servers/"+server.ID, map[string]string{
		"address": "  server.example.com  ", "configuration": "  8 vCPU / 16 GB RAM  ", "notes": "  Primary API  ",
	}, nil, func(w http.ResponseWriter, r *http.Request) {
		api.updateServer(w, r.WithContext(requestContext))
	})
	if response.Code != http.StatusOK {
		t.Fatalf("metadata update returned %d: %s", response.Code, response.Body.String())
	}
	servers, err := database.ListServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Address != "server.example.com" || servers[0].Configuration != "8 vCPU / 16 GB RAM" || servers[0].Notes != "Primary API" {
		t.Fatalf("unexpected updated server: %#v %v", servers, err)
	}
}

func TestRevokeControlPlaneServerReturnsForbidden(t *testing.T) {
	database := openBootstrapTestStore(t)
	server, err := database.EnsureControlPlaneServer(context.Background(), "control-host", "control-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	api := &API{store: database}
	route := chi.NewRouteContext()
	route.URLParams.Add("serverID", server.ID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	response := directJSONRequest(t, http.MethodDelete, "/api/servers/"+server.ID, nil, nil, func(w http.ResponseWriter, r *http.Request) {
		api.revokeServer(w, r.WithContext(requestContext))
	})
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "cannot be revoked") {
		t.Fatalf("unexpected control-plane revoke response: %d %s", response.Code, response.Body.String())
	}
}

func TestNormalizeServerMetadataRejectsOversizedFields(t *testing.T) {
	if _, err := normalizeServerMetadata("", "", strings.Repeat("备", serverNotesLimit)); err != nil {
		t.Fatalf("Unicode notes at the limit should be accepted: %v", err)
	}
	if _, err := normalizeServerMetadata("", "", strings.Repeat("备", serverNotesLimit+1)); err == nil {
		t.Fatal("expected oversized notes to be rejected")
	}
}

func TestDiscoverProjectsQueuesInventoryScan(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "discover-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := directJSONRequest(t, http.MethodPost, "/api/projects/discover", map[string]string{"server_id": server.ID}, &store.Session{UserID: "test-user"}, api.discoverProjects)
	if response.Code != http.StatusAccepted {
		t.Fatalf("project discovery returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(context.Background(), server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "inventory.scan" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
}

func TestDiscoverProjectsRejectsMissingAndOfflineServers(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "offline-token")
	api := resourceTestAPI(database)
	for name, test := range map[string]struct {
		serverID string
		want     int
	}{
		"missing": {serverID: "missing", want: http.StatusNotFound},
		"offline": {serverID: server.ID, want: http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			response := directJSONRequest(t, http.MethodPost, "/api/projects/discover", map[string]string{"server_id": test.serverID}, &store.Session{UserID: "test-user"}, api.discoverProjects)
			if response.Code != test.want {
				t.Fatalf("returned %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestImportProjectReusesExistingProjectOnAnotherServer(t *testing.T) {
	database := openBootstrapTestStore(t)
	serverA := enrollResourceTestServer(t, database, "import-project-a-token")
	serverB := enrollResourceTestServer(t, database, "import-project-b-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, serverA.ID, protocol.Heartbeat{Hostname: "node-a", AgentVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(ctx, serverB.ID, protocol.Heartbeat{Hostname: "node-b", AgentVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	body := map[string]string{"name": "shared", "remote_url": "https://example.com/shared.git"}
	first := map[string]string{"server_id": serverA.ID, "destination": "projects/shared-a"}
	for key, value := range body {
		first[key] = value
	}
	responseA := directJSONRequest(t, http.MethodPost, "/api/projects/import", first, &store.Session{UserID: "test-user"}, api.importProject)
	if responseA.Code != http.StatusAccepted {
		t.Fatalf("server A import returned %d: %s", responseA.Code, responseA.Body.String())
	}
	var queuedA struct {
		Project     store.Project `json:"project"`
		OperationID string        `json:"operation_id"`
	}
	if err := json.Unmarshal(responseA.Body.Bytes(), &queuedA); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: queuedA.OperationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, serverA.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/shared-a", Name: "shared", RemoteURL: body["remote_url"], Branch: "main"}}}); err != nil {
		t.Fatal(err)
	}
	second := map[string]string{"server_id": serverB.ID, "destination": "projects/shared-b"}
	for key, value := range body {
		second[key] = value
	}
	responseB := directJSONRequest(t, http.MethodPost, "/api/projects/import", second, &store.Session{UserID: "test-user"}, api.importProject)
	if responseB.Code != http.StatusAccepted {
		t.Fatalf("server B import returned %d: %s", responseB.Code, responseB.Body.String())
	}

	var queuedB struct {
		Project     store.Project `json:"project"`
		OperationID string        `json:"operation_id"`
	}
	if err := json.Unmarshal(responseB.Body.Bytes(), &queuedB); err != nil {
		t.Fatal(err)
	}
	if queuedA.Project.ID == "" || queuedA.Project.ID != queuedB.Project.ID || queuedA.OperationID == queuedB.OperationID {
		t.Fatalf("imports should share project metadata but not operation IDs: %#v %#v", queuedA, queuedB)
	}
	duplicateA := directJSONRequest(t, http.MethodPost, "/api/projects/import", first, &store.Session{UserID: "test-user"}, api.importProject)
	if duplicateA.Code != http.StatusConflict || !strings.Contains(duplicateA.Body.String(), "already exists on target server") {
		t.Fatalf("existing server A import returned %d: %s", duplicateA.Code, duplicateA.Body.String())
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: queuedB.OperationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, serverB.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/shared-b", Name: "shared", RemoteURL: body["remote_url"], Branch: "main"}}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].WorkspaceCount != 2 {
		t.Fatalf("cross-server import did not retain both workspaces: %#v %v", projects, err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 2 || workspaces[0].ProjectID != queuedA.Project.ID || workspaces[1].ProjectID != queuedA.Project.ID {
		t.Fatalf("unexpected cross-server workspaces: %#v %v", workspaces, err)
	}
	duplicateB := directJSONRequest(t, http.MethodPost, "/api/projects/import", second, &store.Session{UserID: "test-user"}, api.importProject)
	if duplicateB.Code != http.StatusConflict || !strings.Contains(duplicateB.Body.String(), "already exists on target server") {
		t.Fatalf("existing server B import returned %d: %s", duplicateB.Code, duplicateB.Body.String())
	}
}

func TestWorkspaceFilesQueuesAgentScanAndReturnsSnapshot(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-files-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.5"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project", RemoteURL: "https://example.com/project.git"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspace := workspaces[0]
	api := resourceTestAPI(database)

	initial := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspace.ID+"/files", workspace.ID, nil, api.workspaceFiles)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"status":"idle"`) {
		t.Fatalf("unexpected initial snapshot: %d %s", initial.Code, initial.Body.String())
	}
	queued := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/files/refresh", workspace.ID, map[string]any{}, api.refreshWorkspaceFiles)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("file scan returned %d: %s", queued.Code, queued.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "workspace.files" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.WorkspaceFilesCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.WorkspaceID != workspace.ID || command.Path != workspace.Path {
		t.Fatalf("unexpected scan command: %#v %v", command, err)
	}
	snapshot, err := database.WorkspaceFileSnapshot(ctx, workspace.ID)
	if err != nil || snapshot.Status != "scanning" {
		t.Fatalf("unexpected scanning snapshot: %#v %v", snapshot, err)
	}
	if err := database.SaveWorkspaceFiles(ctx, workspace.ID, protocol.WorkspaceFilesResult{Files: []protocol.WorkspaceFile{{Path: "src", Kind: "directory"}, {Path: "src/main.ts", Kind: "file", Size: 12}}, Truncated: true}); err != nil {
		t.Fatal(err)
	}
	completed := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspace.ID+"/files", workspace.ID, nil, api.workspaceFiles)
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"path":"src/main.ts"`) || !strings.Contains(completed.Body.String(), `"truncated":true`) {
		t.Fatalf("unexpected completed snapshot: %d %s", completed.Code, completed.Body.String())
	}
}

func TestWorkspaceFilePreviewQueuesAgentReadAndReturnsContent(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-preview-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.12"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspace := workspaces[0]
	api := resourceTestAPI(database)

	initial := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspace.ID+"/file-preview?path=README.md", workspace.ID, nil, api.workspaceFilePreview)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"status":"idle"`) {
		t.Fatalf("unexpected initial preview: %d %s", initial.Code, initial.Body.String())
	}
	queued := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/file-preview", workspace.ID, map[string]string{"path": "docs/../README.md"}, api.requestWorkspaceFilePreview)
	if queued.Code != http.StatusAccepted || !strings.Contains(queued.Body.String(), `"path":"README.md"`) {
		t.Fatalf("preview returned %d: %s", queued.Code, queued.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "workspace.file.preview" {
		t.Fatalf("unexpected preview operations: %#v %v", operations, err)
	}
	var command protocol.WorkspaceFilePreviewCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.WorkspaceID != workspace.ID || command.Root != workspace.Path || command.Path != "README.md" {
		t.Fatalf("unexpected preview command: %#v %v", command, err)
	}
	if err := database.SaveWorkspaceFilePreview(ctx, workspace.ID, command.Path, protocol.WorkspaceFilePreviewResult{Path: command.Path, Content: "# Project\n", Size: 10}); err != nil {
		t.Fatal(err)
	}
	completed := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspace.ID+"/file-preview?path=README.md", workspace.ID, nil, api.workspaceFilePreview)
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"content":"# Project\n"`) || !strings.Contains(completed.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("unexpected completed preview: %d %s", completed.Code, completed.Body.String())
	}
	invalid := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/file-preview", workspace.ID, map[string]string{"path": "../secret"}, api.requestWorkspaceFilePreview)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid path returned %d: %s", invalid.Code, invalid.Body.String())
	}
	if err := database.BeginWorkspaceFilePreview(ctx, workspace.ID, "src/new.ts"); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveWorkspaceFilePreview(ctx, workspace.ID, "README.md", protocol.WorkspaceFilePreviewResult{Path: "README.md", Content: "stale"}); err != nil {
		t.Fatal(err)
	}
	current, err := database.WorkspaceFilePreview(ctx, workspace.ID, "src/new.ts")
	if err != nil || current.Status != "loading" || current.Content != "" {
		t.Fatalf("stale preview overwrote current selection: %#v %v", current, err)
	}
}

func TestWorkspaceChangesAndDiffPreviewQueueAgentReads(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-changes-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.5"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspace := workspaces[0]
	api := resourceTestAPI(database)

	initial := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspace.ID+"/changes", workspace.ID, nil, api.workspaceChanges)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"status":"idle"`) {
		t.Fatalf("unexpected initial changes: %d %s", initial.Code, initial.Body.String())
	}
	queued := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/changes/refresh", workspace.ID, map[string]any{}, api.refreshWorkspaceChanges)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("change scan returned %d: %s", queued.Code, queued.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "workspace.changes" {
		t.Fatalf("unexpected change operations: %#v %v", operations, err)
	}
	var changesCommand protocol.WorkspaceChangesCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &changesCommand); err != nil || changesCommand.Path != workspace.Path {
		t.Fatalf("unexpected changes command: %#v %v", changesCommand, err)
	}
	changes := protocol.WorkspaceChangesResult{Changes: []protocol.WorkspaceChange{{Path: "src/main.ts", OldPath: "src/old.ts", Status: "renamed", Staged: true}}}
	if err := database.SaveWorkspaceChanges(ctx, workspace.ID, changes); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operations[0].ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	diffQueued := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/diff-preview", workspace.ID, map[string]string{"path": "src/../src/main.ts"}, api.requestWorkspaceDiffPreview)
	if diffQueued.Code != http.StatusAccepted || !strings.Contains(diffQueued.Body.String(), `"path":"src/main.ts"`) {
		t.Fatalf("diff preview returned %d: %s", diffQueued.Code, diffQueued.Body.String())
	}
	operations, err = database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "workspace.diff.preview" {
		t.Fatalf("unexpected diff operations: %#v %v", operations, err)
	}
	var diffCommand protocol.WorkspaceDiffCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &diffCommand); err != nil || diffCommand.Path != "src/main.ts" || diffCommand.OldPath != "src/old.ts" {
		t.Fatalf("unexpected diff command: %#v %v", diffCommand, err)
	}
	if err := database.SaveWorkspaceDiffPreview(ctx, workspace.ID, diffCommand.Path, protocol.WorkspaceDiffResult{Path: diffCommand.Path, Content: "@@ -1 +1 @@\n-old\n+new\n", Additions: 1, Deletions: 1}); err != nil {
		t.Fatal(err)
	}
	completed := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspace.ID+"/diff-preview?path=src/main.ts", workspace.ID, nil, api.workspaceDiffPreview)
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"additions":1`) || !strings.Contains(completed.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("unexpected completed diff: %d %s", completed.Code, completed.Body.String())
	}
	invalid := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/diff-preview", workspace.ID, map[string]string{"path": "../secret"}, api.requestWorkspaceDiffPreview)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid diff path returned %d: %s", invalid.Code, invalid.Body.String())
	}
}

func TestListProjectsIncludesLatestFailedImport(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "import-status-token")
	project, err := database.CreateProject(context.Background(), "tankwar", "https://example.com/tankwar.git")
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueOperation(context.Background(), server.ID, "git.import", protocol.GitImportCommand{ProjectID: project.ID, Name: project.Name, RemoteURL: project.RemoteURL, Destination: "games/tankwar"}, "import-status")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(context.Background(), protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "git clone: HTTP2 framing error"}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ImportStatus != "failed" || projects[0].ImportMessage != "git clone: HTTP2 framing error" || projects[0].ImportServerID != server.ID || projects[0].ImportServerName != server.Name || projects[0].ImportOperationID != operationID {
		t.Fatalf("unexpected project import status: %#v", projects)
	}
}

func TestRetryProjectImportPreservesServerAndDestination(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "retry-import-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(context.Background(), "tankwar", "https://example.com/tankwar.git")
	if err != nil {
		t.Fatal(err)
	}
	failedID, err := database.QueueOperation(context.Background(), server.ID, "git.import", protocol.GitImportCommand{ProjectID: project.ID, Name: project.Name, RemoteURL: project.RemoteURL, Destination: "games/tankwar"}, "retry-original")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(context.Background(), protocol.OperationResult{OperationID: failedID, Status: "failed", Message: "network timeout"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := projectResourceRequest(t, http.MethodPost, "/api/projects/"+project.ID+"/retry-import", project.ID, map[string]any{}, api.retryProjectImport)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry returned %d: %s", response.Code, response.Body.String())
	}
	latest, err := database.LatestProjectImport(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID == failedID || latest.Status != "queued" || latest.ServerID != server.ID || latest.Command.Destination != "games/tankwar" || latest.Command.Name != project.Name || latest.Command.RemoteURL != project.RemoteURL {
		t.Fatalf("unexpected retried import: %#v", latest)
	}
}

func TestRetryProjectImportRejectsNonFailedImport(t *testing.T) {
	for _, status := range []string{"queued", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			database := openBootstrapTestStore(t)
			server := enrollResourceTestServer(t, database, "retry-"+status+"-token")
			if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1"}); err != nil {
				t.Fatal(err)
			}
			project, err := database.CreateProject(context.Background(), "project-"+status, "https://example.com/"+status+".git")
			if err != nil {
				t.Fatal(err)
			}
			operationID, err := database.QueueOperation(context.Background(), server.ID, "git.import", protocol.GitImportCommand{ProjectID: project.ID}, "retry-"+status)
			if err != nil {
				t.Fatal(err)
			}
			if status != "queued" {
				if err := database.CompleteOperation(context.Background(), protocol.OperationResult{OperationID: operationID, Status: status}); err != nil {
					t.Fatal(err)
				}
			}
			api := resourceTestAPI(database)
			response := projectResourceRequest(t, http.MethodPost, "/api/projects/"+project.ID+"/retry-import", project.ID, map[string]any{}, api.retryProjectImport)
			if response.Code != http.StatusConflict {
				t.Fatalf("retry returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDeleteProjectRejectsActiveImportAndWorkspace(t *testing.T) {
	t.Run("active import", func(t *testing.T) {
		database := openBootstrapTestStore(t)
		server := enrollResourceTestServer(t, database, "delete-active-token")
		project, err := database.CreateProject(context.Background(), "active", "https://example.com/active.git")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.QueueOperation(context.Background(), server.ID, "git.import", protocol.GitImportCommand{ProjectID: project.ID}, "delete-active"); err != nil {
			t.Fatal(err)
		}
		api := resourceTestAPI(database)
		response := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, nil, api.deleteProject)
		if response.Code != http.StatusConflict {
			t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("workspace", func(t *testing.T) {
		database := openBootstrapTestStore(t)
		server := enrollResourceTestServer(t, database, "delete-workspace-token")
		project, err := database.CreateProject(context.Background(), "workspace", "https://example.com/workspace.git")
		if err != nil {
			t.Fatal(err)
		}
		if err := database.UpsertInventory(context.Background(), server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/workspace", Name: project.Name, RemoteURL: project.RemoteURL, Branch: "main"}}}); err != nil {
			t.Fatal(err)
		}
		api := resourceTestAPI(database)
		response := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, nil, api.deleteProject)
		if response.Code != http.StatusConflict {
			t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
		}
	})
}

func TestDeleteFailedProjectWithoutWorkspace(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "delete-failed-token")
	project, err := database.CreateProject(context.Background(), "failed", "https://example.com/failed.git")
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueOperation(context.Background(), server.ID, "git.import", protocol.GitImportCommand{ProjectID: project.ID}, "delete-failed")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(context.Background(), protocol.OperationResult{OperationID: operationID, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, nil, api.deleteProject)
	if response.Code != http.StatusOK {
		t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
	}
	if _, err := database.Project(context.Background(), project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project still exists: %v", err)
	}
}

func TestStartTurnQueuesSelectedModelAndReasoningEffort(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "turn-model-token")
	if err := database.UpsertInventory(context.Background(), server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/model-project", Name: "model-project", RemoteURL: "https://example.com/model-project.git", Branch: "main"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(context.Background())
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(context.Background(), workspaces[0].ID, "model test")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	image := "data:image/png;base64,iVBORw0KGgo="
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/turns", thread.ID, map[string]any{"prompt": "hello", "images": []map[string]string{{"data_url": image}}, "model": "  gpt-5.6-sol  ", "reasoning_effort": "  high  ", "approval_mode": "on-request"}, api.startTurn)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start turn returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(context.Background(), server.ID)
	if err != nil || len(operations) != 1 {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.StartTurnCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.Model != "gpt-5.6-sol" {
		t.Fatalf("unexpected model: %q", command.Model)
	}
	if command.ReasoningEffort != "high" {
		t.Fatalf("unexpected reasoning effort: %q", command.ReasoningEffort)
	}
	if len(command.Images) != 1 || command.Images[0].DataURL != image {
		t.Fatalf("unexpected turn images: %#v", command.Images)
	}
	duplicate := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/turns", thread.ID, map[string]any{"prompt": "duplicate", "approval_mode": "on-request"}, api.startTurn)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate active turn returned %d: %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestRewriteTurnPreservesHistoryUntilForkIsAccepted(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "rewrite-turn-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/rewrite", Name: "rewrite"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "rewrite test")
	if err != nil {
		t.Fatal(err)
	}
	add := func(kind, payload string) protocol.StreamEvent {
		t.Helper()
		event, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: kind, Payload: json.RawMessage(payload)})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	add("user.message", `{"text":"first"}`)
	add("turn.accepted", `{"turn_id":"turn-1"}`)
	add("codex.item.completed", `{"item":{"type":"agentMessage","text":"first answer"}}`)
	add("codex.turn.completed", `{"turn":{"status":"completed"}}`)
	target := add("user.message", `{"text":"second"}`)
	add("turn.accepted", `{"turn_id":"turn-2"}`)
	add("codex.item.completed", `{"item":{"type":"agentMessage","text":"second answer"}}`)
	add("codex.turn.completed", `{"turn":{"status":"interrupted"}}`)
	add("user.message", `{"text":"third"}`)
	add("turn.accepted", `{"turn_id":"turn-3"}`)
	add("codex.item.completed", `{"item":{"type":"agentMessage","text":"third answer"}}`)

	api := resourceTestAPI(database)
	response := rewriteResourceRequest(t, thread.ID, target.EventID, map[string]any{"prompt": "revised second", "approval_mode": "on-request"}, api.rewriteTurn)
	if response.Code != http.StatusAccepted {
		t.Fatalf("rewrite returned %d: %s", response.Code, response.Body.String())
	}
	events, err := database.Events(ctx, thread.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 11 || events[4].EventID != target.EventID || !strings.Contains(string(events[4].Payload), "second") {
		t.Fatalf("rewrite changed history before Codex accepted the fork: %#v", events)
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.turn.rewrite" {
		t.Fatalf("unexpected rewrite operations: %#v %v", operations, err)
	}
	var command protocol.RewriteTurnCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.NumTurns != 2 || command.Start.Prompt != "revised second" || command.Start.ThreadID != thread.ID || command.EditEventID != target.EventID || command.ReplacementEventID == "" || command.CutoffSequence != 11 {
		t.Fatalf("unexpected rewrite command: %#v", command)
	}
	updated, err := database.Thread(ctx, thread.ID)
	if err != nil || updated.Status != "queued" {
		t.Fatalf("rewrite did not queue thread: %#v %v", updated, err)
	}
	postCutoff, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.item.completed", Payload: json.RawMessage(`{"item":{"type":"agentMessage","text":"replacement answer"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	replacement, changed, err := database.CommitThreadRewrite(ctx, thread.ID, "forked-thread", command.EditEventID, command.ReplacementEventID, command.ReplacementPayload, command.CutoffSequence)
	if err != nil || !changed {
		t.Fatalf("could not commit accepted rewrite: changed=%v event=%#v err=%v", changed, replacement, err)
	}
	events, err = database.Events(ctx, thread.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 || events[4].EventID != command.ReplacementEventID || events[4].Sequence != target.Sequence || !strings.Contains(string(events[4].Payload), "revised second") || events[5].EventID != postCutoff.EventID {
		t.Fatalf("accepted rewrite did not replace only the old branch: %#v", events)
	}
	updated, err = database.Thread(ctx, thread.ID)
	if err != nil || updated.CodexThreadID != "forked-thread" || updated.Status != "running" {
		t.Fatalf("accepted rewrite did not bind fork: %#v %v", updated, err)
	}
	conflict := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/turns", thread.ID, map[string]any{"prompt": "again", "approval_mode": "on-request", "edit_event_id": replacement.EventID}, api.startTurn)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("active rewrite returned %d: %s", conflict.Code, conflict.Body.String())
	}
}

func TestInterruptQueuesTheCurrentlyAcceptedTurnID(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "interrupt-turn-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/interrupt", Name: "interrupt"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "interrupt test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "turn.accepted", Payload: json.RawMessage(`{"turn_id":"old-turn"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.started", Payload: json.RawMessage(`{"threadId":"codex-thread","turn":{"id":"captured-turn"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetThreadStatus(ctx, thread.ID, "running"); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/interrupt", thread.ID, nil, api.interruptTurn)
	if response.Code != http.StatusAccepted {
		t.Fatalf("interrupt returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 {
		t.Fatalf("unexpected interrupt operations: %#v %v", operations, err)
	}
	var command protocol.InterruptTurnCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.TurnID != "captured-turn" {
		t.Fatalf("interrupt did not capture the active turn: %#v", command)
	}
}

func TestInterruptCancelsQueuedTurnBeforeDelivery(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "cancel-queued-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/cancel", Name: "cancel"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "cancel queued")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ClaimThreadForTurn(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueOperation(ctx, server.ID, "codex.turn.start", protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "cancel me"}, "cancel-queued-operation")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/interrupt", thread.ID, nil, api.interruptTurn)
	if response.Code != http.StatusAccepted {
		t.Fatalf("queued interrupt returned %d: %s", response.Code, response.Body.String())
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "cancelled" {
		t.Fatalf("queued operation was not cancelled: %#v %v", operation, err)
	}
	updated, err := database.Thread(ctx, thread.ID)
	if err != nil || updated.Status != "idle" {
		t.Fatalf("thread was not released after cancellation: %#v %v", updated, err)
	}
	events, err := database.Events(ctx, thread.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "codex.turn.cancelled" {
		t.Fatalf("unexpected cancellation event: %#v %v", events, err)
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	operation, err = database.Operation(ctx, operationID)
	if err != nil || operation.Status != "cancelled" {
		t.Fatalf("late result overwrote cancelled operation: %#v %v", operation, err)
	}
}

func TestCreateThreadIgnoresLegacyClientTitle(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "create-thread-title")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	api := resourceTestAPI(database)
	response := directJSONRequest(t, http.MethodPost, "/api/threads", map[string]string{"workspace_id": workspaces[0].ID, "title": "legacy custom title"}, &store.Session{UserID: "test-user"}, api.createThread)
	if response.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}
	var thread store.Thread
	if err := json.Unmarshal(response.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Title != "New session" {
		t.Fatalf("legacy client title was not ignored: %q", thread.Title)
	}
}

func TestDeleteThreadCleansEventsAndRejectsActiveSessions(t *testing.T) {
	for _, status := range []string{"idle", "running"} {
		t.Run(status, func(t *testing.T) {
			database := openBootstrapTestStore(t)
			server := enrollResourceTestServer(t, database, "delete-thread-"+status)
			ctx := context.Background()
			if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, err := database.ListWorkspaces(ctx)
			if err != nil || len(workspaces) != 1 {
				t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
			}
			thread, err := database.CreateThread(ctx, workspaces[0].ID, "delete me")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "user.message", Payload: json.RawMessage(`{"text":"hello"}`)}); err != nil {
				t.Fatal(err)
			}
			if status == "running" {
				if err := database.SetThreadStatus(ctx, thread.ID, status); err != nil {
					t.Fatal(err)
				}
			}
			api := resourceTestAPI(database)
			response := threadResourceRequest(t, http.MethodDelete, "/api/threads/"+thread.ID, thread.ID, nil, api.deleteThread)
			if status == "running" {
				if response.Code != http.StatusConflict {
					t.Fatalf("active delete returned %d: %s", response.Code, response.Body.String())
				}
				if _, err := database.Thread(ctx, thread.ID); err != nil {
					t.Fatalf("active thread was deleted: %v", err)
				}
				return
			}
			if response.Code != http.StatusOK {
				t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
			}
			if _, err := database.Thread(ctx, thread.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("thread still exists: %v", err)
			}
			events, err := database.Events(ctx, thread.ID, 0, 10)
			if err != nil || len(events) != 0 {
				t.Fatalf("thread events were not deleted: %#v %v", events, err)
			}
		})
	}
}

func TestThreadsSupportOptionalBoundedPagination(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	server := enrollResourceTestServer(t, database, "thread-pagination")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/thread-pagination", Name: "thread-pagination"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	var threads []store.Thread
	for _, title := range []string{"oldest", "middle", "newest", "archived"} {
		thread, createErr := database.CreateThread(ctx, workspaces[0].ID, title)
		if createErr != nil {
			t.Fatal(createErr)
		}
		threads = append(threads, thread)
	}
	base := time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC)
	for index, thread := range threads[:3] {
		if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET updated_at=? WHERE id=?"), base.Add(time.Duration(index)*time.Minute), thread.ID); err != nil {
			t.Fatal(err)
		}
	}
	archived := true
	if _, err := database.UpdateThread(ctx, threads[3].ID, nil, nil, &archived); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)

	compatibility := directJSONRequest(t, http.MethodGet, "/api/threads", nil, nil, api.threads)
	if compatibility.Code != http.StatusOK {
		t.Fatalf("legacy listing returned %d: %s", compatibility.Code, compatibility.Body.String())
	}
	var legacy []store.Thread
	if err := json.Unmarshal(compatibility.Body.Bytes(), &legacy); err != nil || len(legacy) != 3 || legacy[0].ID != threads[2].ID || legacy[1].ID != threads[1].ID || legacy[2].ID != threads[0].ID {
		t.Fatalf("legacy response must remain an ordered array: %#v %v", legacy, err)
	}

	firstResponse := directJSONRequest(t, http.MethodGet, "/api/threads?limit=2", nil, nil, api.threads)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first page returned %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	var first threadListPageResponse
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || first.Next == nil || *first.Next != 2 || len(first.Items) != 2 || first.Items[0].ID != threads[2].ID || first.Items[1].ID != threads[1].ID {
		t.Fatalf("unexpected first page: %#v", first)
	}
	secondResponse := directJSONRequest(t, http.MethodGet, "/api/threads?limit=2&offset=2", nil, nil, api.threads)
	var second threadListPageResponse
	if secondResponse.Code != http.StatusOK || json.Unmarshal(secondResponse.Body.Bytes(), &second) != nil || second.HasMore || second.Next != nil || len(second.Items) != 1 || second.Items[0].ID != threads[0].ID {
		t.Fatalf("unexpected second page: %d %#v", secondResponse.Code, second)
	}
	defaultLimitResponse := directJSONRequest(t, http.MethodGet, "/api/threads?offset=1", nil, nil, api.threads)
	var defaultLimit threadListPageResponse
	if defaultLimitResponse.Code != http.StatusOK || json.Unmarshal(defaultLimitResponse.Body.Bytes(), &defaultLimit) != nil || defaultLimit.HasMore || defaultLimit.Next != nil || len(defaultLimit.Items) != 2 {
		t.Fatalf("offset-only pagination did not use the default limit: %d %#v", defaultLimitResponse.Code, defaultLimit)
	}
	archivedResponse := directJSONRequest(t, http.MethodGet, "/api/threads?archived=true&limit=1", nil, nil, api.threads)
	var archivedPage threadListPageResponse
	if archivedResponse.Code != http.StatusOK || json.Unmarshal(archivedResponse.Body.Bytes(), &archivedPage) != nil || archivedPage.HasMore || archivedPage.Next != nil || len(archivedPage.Items) != 1 || archivedPage.Items[0].ID != threads[3].ID {
		t.Fatalf("archived pagination filter failed: %d %#v", archivedResponse.Code, archivedPage)
	}
	allResponse := directJSONRequest(t, http.MethodGet, "/api/threads?archived=all&limit=10", nil, nil, api.threads)
	var allPage threadListPageResponse
	if allResponse.Code != http.StatusOK || json.Unmarshal(allResponse.Body.Bytes(), &allPage) != nil || allPage.HasMore || len(allPage.Items) != 4 {
		t.Fatalf("all-thread pagination filter failed: %d %#v", allResponse.Code, allPage)
	}

	for _, target := range []string{
		"/api/threads?limit=", "/api/threads?limit=0", "/api/threads?limit=-1", "/api/threads?limit=one", "/api/threads?limit=1&limit=2",
		"/api/threads?offset=", "/api/threads?offset=-1", "/api/threads?offset=one", "/api/threads?offset=1&offset=2",
	} {
		response := directJSONRequest(t, http.MethodGet, target, nil, nil, api.threads)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid pagination %q returned %d: %s", target, response.Code, response.Body.String())
		}
	}

	for count := len(legacy); count <= maxThreadsPageLimit; count++ {
		if _, err := database.CreateThread(ctx, workspaces[0].ID, "capped"); err != nil {
			t.Fatal(err)
		}
	}
	cappedResponse := directJSONRequest(t, http.MethodGet, "/api/threads?limit=1000", nil, nil, api.threads)
	var capped threadListPageResponse
	if cappedResponse.Code != http.StatusOK || json.Unmarshal(cappedResponse.Body.Bytes(), &capped) != nil || !capped.HasMore || capped.Next == nil || *capped.Next != maxThreadsPageLimit || len(capped.Items) != maxThreadsPageLimit {
		t.Fatalf("page limit was not capped: %d %#v", cappedResponse.Code, capped)
	}
}

func TestValidTurnImages(t *testing.T) {
	valid := protocol.TurnImage{DataURL: "data:image/png;base64,iVBORw0KGgo="}
	if !validTurnImages([]protocol.TurnImage{valid}) {
		t.Fatal("valid image was rejected")
	}
	if validTurnImages([]protocol.TurnImage{{DataURL: "data:image/svg+xml;base64,Zm9v"}}) {
		t.Fatal("unsupported image type was accepted")
	}
	if validTurnImages([]protocol.TurnImage{{DataURL: "data:image/png;base64,not-base64"}}) {
		t.Fatal("invalid base64 was accepted")
	}
}

func TestAuditLogSupportsOptionalBoundedPagination(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, time.August, 2, 13, 0, 0, 0, time.UTC)
	for index, action := range []string{"oldest", "middle", "newest"} {
		if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO audit_log(id,user_id,action,resource_type,resource_id,detail,ip_address,occurred_at) VALUES(?,?,?,?,?,?,?,?)"), "audit-"+action, "user", action, "thread", action, `{}`, "127.0.0.1", base.Add(time.Duration(index)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	api := resourceTestAPI(database)

	legacyResponse := directJSONRequest(t, http.MethodGet, "/api/audit", nil, nil, api.auditLog)
	var legacy []auditEntry
	if legacyResponse.Code != http.StatusOK || json.Unmarshal(legacyResponse.Body.Bytes(), &legacy) != nil || len(legacy) != 3 || legacy[0].Action != "newest" || legacy[2].Action != "oldest" {
		t.Fatalf("legacy audit response must remain an ordered array: %d %#v", legacyResponse.Code, legacy)
	}

	firstResponse := directJSONRequest(t, http.MethodGet, "/api/audit?limit=2", nil, nil, api.auditLog)
	var first auditListPageResponse
	if firstResponse.Code != http.StatusOK || json.Unmarshal(firstResponse.Body.Bytes(), &first) != nil || !first.HasMore || first.Next == nil || *first.Next != 2 || len(first.Items) != 2 || first.Items[0].Action != "newest" || first.Items[1].Action != "middle" {
		t.Fatalf("unexpected first audit page: %d %#v", firstResponse.Code, first)
	}
	secondResponse := directJSONRequest(t, http.MethodGet, "/api/audit?limit=2&offset=2", nil, nil, api.auditLog)
	var second auditListPageResponse
	if secondResponse.Code != http.StatusOK || json.Unmarshal(secondResponse.Body.Bytes(), &second) != nil || second.HasMore || second.Next != nil || len(second.Items) != 1 || second.Items[0].Action != "oldest" {
		t.Fatalf("unexpected second audit page: %d %#v", secondResponse.Code, second)
	}

	for _, target := range []string{"/api/audit?limit=", "/api/audit?limit=0", "/api/audit?limit=-1", "/api/audit?limit=one", "/api/audit?limit=1&limit=2", "/api/audit?offset=", "/api/audit?offset=-1", "/api/audit?offset=one", "/api/audit?offset=1&offset=2"} {
		response := directJSONRequest(t, http.MethodGet, target, nil, nil, api.auditLog)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid audit pagination %q returned %d: %s", target, response.Code, response.Body.String())
		}
	}

	for index := 0; index < 101; index++ {
		if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO audit_log(id,user_id,action,resource_type,resource_id,detail,ip_address,occurred_at) VALUES(?,?,?,?,?,?,?,?)"), "audit-cap-"+strconv.Itoa(index), "user", "cap", "thread", "cap", `{}`, "127.0.0.1", base.Add(time.Duration(index+10)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	cappedResponse := directJSONRequest(t, http.MethodGet, "/api/audit?limit=1000", nil, nil, api.auditLog)
	var capped auditListPageResponse
	if cappedResponse.Code != http.StatusOK || json.Unmarshal(cappedResponse.Body.Bytes(), &capped) != nil || !capped.HasMore || capped.Next == nil || *capped.Next != maxAuditPageLimit || len(capped.Items) != maxAuditPageLimit {
		t.Fatalf("audit page limit was not capped: %d %#v", cappedResponse.Code, capped)
	}
}

func TestStartTurnRejectsOversizedModel(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/missing/turns", "missing", map[string]string{"prompt": "hello", "model": strings.Repeat("m", 129)}, api.startTurn)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("start turn returned %d: %s", response.Code, response.Body.String())
	}
}

func TestStartTurnRejectsInvalidReasoningEffort(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/missing/turns", "missing", map[string]string{"prompt": "hello", "reasoning_effort": "extreme"}, api.startTurn)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("start turn returned %d: %s", response.Code, response.Body.String())
	}
}

func TestThreadEventsUseRecentBoundedWindows(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	server := enrollResourceTestServer(t, database, "thread-events-window-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/thread-events-window", Name: "thread-events-window"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "long thread")
	if err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 1001; sequence++ {
		if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "user.message", Payload: json.RawMessage(`{"text":"message"}`)}); err != nil {
			t.Fatal(err)
		}
	}

	api := resourceTestAPI(database)
	for name, test := range map[string]struct {
		target string
		count  int
		first  int64
	}{
		"conversation default":              {target: "/api/threads/" + thread.ID + "/events?view=conversation", count: 500, first: 502},
		"raw default":                       {target: "/api/threads/" + thread.ID + "/events?view=raw", count: 500, first: 502},
		"raw before uses default":           {target: "/api/threads/" + thread.ID + "/events?view=raw&before=1002", count: 500, first: 502},
		"non-positive limit uses default":   {target: "/api/threads/" + thread.ID + "/events?view=raw&limit=0", count: 500, first: 502},
		"oversized limit is capped at 1000": {target: "/api/threads/" + thread.ID + "/events?view=conversation&limit=1001", count: 1000, first: 2},
		"before oversized limit is capped":  {target: "/api/threads/" + thread.ID + "/events?view=conversation&before=1002&limit=1001", count: 1000, first: 2},
	} {
		t.Run(name, func(t *testing.T) {
			response := threadResourceRequest(t, http.MethodGet, test.target, thread.ID, nil, api.threadEvents)
			if response.Code != http.StatusOK {
				t.Fatalf("thread events returned %d: %s", response.Code, response.Body.String())
			}
			var events []protocol.StreamEvent
			if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
				t.Fatal(err)
			}
			if len(events) != test.count || events[0].Sequence != test.first || events[len(events)-1].Sequence != 1001 {
				t.Fatalf("expected bounded events in sequence order, got first=%#v last=%#v len=%d", events[0], events[len(events)-1], len(events))
			}
		})
	}
}

func TestThreadEventsSupportCursorsViewsAndLimits(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	server := enrollResourceTestServer(t, database, "thread-events-cursor-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/thread-events-cursor", Name: "thread-events-cursor"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "cursor thread")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agent.progress", "user.message", "codex.item.started", "agent.progress", "codex.item.completed", "agent.progress", "codex.turn.completed"} {
		if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: kind, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}

	api := resourceTestAPI(database)
	for name, test := range map[string]struct {
		target    string
		sequences []int64
		status    int
	}{
		"conversation recent window filters raw events": {target: "/api/threads/" + thread.ID + "/events?view=conversation&limit=2", sequences: []int64{5, 7}, status: http.StatusOK},
		"raw recent window includes every event":        {target: "/api/threads/" + thread.ID + "/events?view=raw&limit=3", sequences: []int64{5, 6, 7}, status: http.StatusOK},
		"conversation after cursor":                     {target: "/api/threads/" + thread.ID + "/events?view=conversation&after=3&limit=2", sequences: []int64{5, 7}, status: http.StatusOK},
		"raw after cursor":                              {target: "/api/threads/" + thread.ID + "/events?view=raw&after=3&limit=2", sequences: []int64{4, 5}, status: http.StatusOK},
		"zero is a valid cursor":                        {target: "/api/threads/" + thread.ID + "/events?view=conversation&after=0&limit=2", sequences: []int64{2, 3}, status: http.StatusOK},
		"invalid cursor is rejected":                    {target: "/api/threads/" + thread.ID + "/events?view=raw&after=invalid", status: http.StatusBadRequest},
		"negative cursor is rejected":                   {target: "/api/threads/" + thread.ID + "/events?view=raw&after=-1", status: http.StatusBadRequest},
		"conversation before cursor filters raw events": {target: "/api/threads/" + thread.ID + "/events?view=conversation&before=6&limit=2", sequences: []int64{3, 5}, status: http.StatusOK},
		"raw before cursor":                             {target: "/api/threads/" + thread.ID + "/events?view=raw&before=6&limit=2", sequences: []int64{4, 5}, status: http.StatusOK},
		"before cursor excludes its sequence":           {target: "/api/threads/" + thread.ID + "/events?view=raw&before=5&limit=2", sequences: []int64{3, 4}, status: http.StatusOK},
		"before at first sequence is empty":             {target: "/api/threads/" + thread.ID + "/events?view=raw&before=1&limit=2", sequences: []int64{}, status: http.StatusOK},
		"zero is a valid before cursor":                 {target: "/api/threads/" + thread.ID + "/events?view=raw&before=0&limit=2", sequences: []int64{}, status: http.StatusOK},
		"invalid before cursor is rejected":             {target: "/api/threads/" + thread.ID + "/events?view=raw&before=invalid", status: http.StatusBadRequest},
		"negative before cursor is rejected":            {target: "/api/threads/" + thread.ID + "/events?view=raw&before=-1", status: http.StatusBadRequest},
		"empty before cursor is rejected":               {target: "/api/threads/" + thread.ID + "/events?view=raw&before=", status: http.StatusBadRequest},
		"before and after are mutually exclusive":       {target: "/api/threads/" + thread.ID + "/events?view=raw&before=6&after=3", status: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			response := threadResourceRequest(t, http.MethodGet, test.target, thread.ID, nil, api.threadEvents)
			if response.Code != test.status {
				t.Fatalf("thread events returned %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if response.Code != http.StatusOK {
				return
			}
			var events []protocol.StreamEvent
			if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
				t.Fatal(err)
			}
			if len(events) != len(test.sequences) {
				t.Fatalf("event count = %d, want %d: %#v", len(events), len(test.sequences), events)
			}
			for index, event := range events {
				if event.Sequence != test.sequences[index] {
					t.Fatalf("event %d sequence = %d, want %d: %#v", index, event.Sequence, test.sequences[index], events)
				}
			}
		})
	}
}

func TestUpdateProjectReturnsPreferencesAndWritesAudit(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "update-project-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/update-project", Name: "before"}}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("unexpected projects: %#v %v", projects, err)
	}
	api := resourceTestAPI(database)
	response := projectResourceRequest(t, http.MethodPatch, "/api/projects/"+projects[0].ID, projects[0].ID, map[string]any{"name": "  after  ", "description": "  project details  ", "default_branch": "trunk", "pinned": true, "hidden": true, "archived": true}, api.updateProject)
	if response.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", response.Code, response.Body.String())
	}
	var updated store.Project
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "after" || updated.Description != "project details" || updated.DefaultBranch != "trunk" || updated.PinnedAt == nil || updated.HiddenAt == nil || updated.ArchivedAt == nil || updated.Status != "archived" {
		t.Fatalf("unexpected updated project: %#v", updated)
	}
	projects, err = database.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].HiddenAt == nil {
		t.Fatalf("hidden project was omitted from project list: %#v %v", projects, err)
	}
	var auditCount int
	if err := database.DB.GetContext(ctx, &auditCount, database.Q("SELECT COUNT(*) FROM audit_log WHERE action='project.update' AND resource_id=?"), updated.ID); err != nil || auditCount != 1 {
		t.Fatalf("project audit missing: count=%d err=%v", auditCount, err)
	}
	response = projectResourceRequest(t, http.MethodPatch, "/api/projects/"+updated.ID, updated.ID, map[string]any{"pinned": false, "hidden": false, "archived": false}, api.updateProject)
	if response.Code != http.StatusOK {
		t.Fatalf("clear preferences returned %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.PinnedAt != nil || updated.HiddenAt != nil || updated.ArchivedAt != nil || updated.Status == "archived" {
		t.Fatalf("project preferences were not cleared: %#v", updated)
	}
}

func TestProjectDetailIncludesRemotesAndOperations(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "project-detail-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project-detail", Name: "detail"}}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("unexpected projects: %#v %v", projects, err)
	}
	projectID := projects[0].ID
	api := resourceTestAPI(database)
	empty := projectResourceRequest(t, http.MethodGet, "/api/projects/"+projectID, projectID, nil, api.projectDetail)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty detail returned %d: %s", empty.Code, empty.Body.String())
	}
	var emptyDetail struct {
		Remotes    []store.ProjectRemote `json:"remotes"`
		Operations []store.Operation     `json:"operations"`
	}
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyDetail); err != nil {
		t.Fatal(err)
	}
	if emptyDetail.Remotes == nil || emptyDetail.Operations == nil {
		t.Fatalf("empty detail collections must be arrays: %s", empty.Body.String())
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO project_remotes(id,project_id,name,mode,provider,fetch_url,push_url,status) VALUES(?,?,?,?,?,?,?,?)"), store.NewID(), projectID, "origin", "existing", "github", "https://github.com/example/detail.git", "https://github.com/example/detail.git", "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueResourceOperation(ctx, server.ID, "git.status", map[string]string{"project_id": projectID}, "detail-operation", store.OperationResource{ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	response := projectResourceRequest(t, http.MethodGet, "/api/projects/"+projectID, projectID, nil, api.projectDetail)
	if response.Code != http.StatusOK {
		t.Fatalf("detail returned %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		Project    store.Project         `json:"project"`
		Remotes    []store.ProjectRemote `json:"remotes"`
		Operations []store.Operation     `json:"operations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Project.ID != projectID || len(detail.Remotes) != 1 || detail.Remotes[0].FetchURL == "" || len(detail.Operations) != 1 || detail.Operations[0].Kind != "git.status" {
		t.Fatalf("unexpected detail response: %#v", detail)
	}
	history := projectResourceRequest(t, http.MethodGet, "/api/projects/"+projectID+"/operations?limit=1", projectID, nil, api.projectOperations)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "git.status") {
		t.Fatalf("unexpected history response: %d %s", history.Code, history.Body.String())
	}
}

func TestWorkspaceGitRefreshQueuesInspectAndReadsSnapshot(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-git-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "workspace-git", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/workspace-git", Name: "git-view", Branch: "main", CommitSHA: "abc", Dirty: true}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspaceID := workspaces[0].ID
	api := resourceTestAPI(database)
	emptySnapshotResponse := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspaceID+"/git", workspaceID, nil, api.workspaceGit)
	if emptySnapshotResponse.Code != http.StatusOK {
		t.Fatalf("empty Git snapshot returned %d: %s", emptySnapshotResponse.Code, emptySnapshotResponse.Body.String())
	}
	var emptySnapshot store.WorkspaceGitSnapshot
	if err := json.Unmarshal(emptySnapshotResponse.Body.Bytes(), &emptySnapshot); err != nil {
		t.Fatal(err)
	}
	if emptySnapshot.Data.Changes == nil || emptySnapshot.Data.Branches == nil || emptySnapshot.Data.Remotes == nil || emptySnapshot.Data.Commits == nil {
		t.Fatalf("empty Git snapshot collections must be arrays: %s", emptySnapshotResponse.Body.String())
	}
	refresh := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspaceID+"/git/refresh", workspaceID, map[string]any{}, api.refreshWorkspaceGit)
	if refresh.Code != http.StatusAccepted {
		t.Fatalf("refresh returned %d: %s", refresh.Code, refresh.Body.String())
	}
	var accepted map[string]string
	if err := json.Unmarshal(refresh.Body.Bytes(), &accepted); err != nil || accepted["operation_id"] == "" {
		t.Fatalf("unexpected refresh response: %s", refresh.Body.String())
	}
	operation, err := database.Operation(ctx, accepted["operation_id"])
	if err != nil || operation.Kind != "git.workspace.inspect" || operation.WorkspaceID != workspaceID {
		t.Fatalf("unexpected refresh operation: %#v %v", operation, err)
	}
	refreshingSnapshotResponse := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspaceID+"/git", workspaceID, nil, api.workspaceGit)
	if err := json.Unmarshal(refreshingSnapshotResponse.Body.Bytes(), &emptySnapshot); err != nil {
		t.Fatal(err)
	}
	if emptySnapshot.Data.Changes == nil || emptySnapshot.Data.Branches == nil || emptySnapshot.Data.Remotes == nil || emptySnapshot.Data.Commits == nil {
		t.Fatalf("refreshing Git snapshot collections must be arrays: %s", refreshingSnapshotResponse.Body.String())
	}
	result := protocol.GitWorkspaceInspectResult{WorkspaceID: workspaceID, Status: protocol.GitStatus{Branch: "main", Head: "abc", Dirty: true}, Commits: []protocol.GitCommit{{SHA: "abc", Title: "latest"}}}
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspaceID, result); err != nil {
		t.Fatal(err)
	}
	snapshotResponse := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspaceID+"/git", workspaceID, nil, api.workspaceGit)
	if snapshotResponse.Code != http.StatusOK || !strings.Contains(snapshotResponse.Body.String(), "latest") {
		t.Fatalf("unexpected Git snapshot response: %d %s", snapshotResponse.Code, snapshotResponse.Body.String())
	}
	commitsResponse := workspaceResourceRequest(t, http.MethodGet, "/api/workspaces/"+workspaceID+"/git/commits?limit=1", workspaceID, nil, api.workspaceGitCommits)
	if commitsResponse.Code != http.StatusOK || !strings.Contains(commitsResponse.Body.String(), "latest") {
		t.Fatalf("unexpected Git commits response: %d %s", commitsResponse.Code, commitsResponse.Body.String())
	}
}

func TestWorkspaceGitChangeActionsQueueStructuredWriteCommands(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "workspace-git-write-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "workspace-git-write", AgentVersion: "0.2.36"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/workspace-git-write", Name: "git-write", Branch: "main", CommitSHA: "abc", Dirty: true}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspaceID := workspaces[0].ID
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspaceID); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	tests := []struct {
		name              string
		path              string
		body              map[string]any
		handler           http.HandlerFunc
		wantAction        string
		wantPath          string
		wantAll           bool
		wantIncludeStaged bool
		wantMessage       string
	}{
		{name: "stage file", path: "/api/workspaces/" + workspaceID + "/git/stage", body: map[string]any{"paths": []string{"src/new.ts"}}, handler: api.stageGitChanges, wantAction: "stage", wantPath: "src/new.ts"},
		{name: "unstage all", path: "/api/workspaces/" + workspaceID + "/git/unstage", body: map[string]any{"all": true}, handler: api.unstageGitChanges, wantAction: "unstage", wantAll: true},
		{name: "discard file", path: "/api/workspaces/" + workspaceID + "/git/discard", body: map[string]any{"paths": []string{"src/old.ts"}}, handler: api.discardGitChanges, wantAction: "discard", wantPath: "src/old.ts"},
		{name: "discard reviewed staged file", path: "/api/workspaces/" + workspaceID + "/git/discard", body: map[string]any{"paths": []string{"src/staged.ts"}, "include_staged": true}, handler: api.discardGitChanges, wantAction: "discard", wantPath: "src/staged.ts", wantIncludeStaged: true},
		{name: "commit", path: "/api/workspaces/" + workspaceID + "/git/commit", body: map[string]any{"message": "Update Git workspace"}, handler: api.commitGitChanges, wantAction: "commit", wantMessage: "Update Git workspace"},
		{name: "commit all", path: "/api/workspaces/" + workspaceID + "/git/commit", body: map[string]any{"message": "Commit every change", "all": true}, handler: api.commitGitChanges, wantAction: "commit", wantAll: true, wantMessage: "Commit every change"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := workspaceResourceRequest(t, http.MethodPost, test.path, workspaceID, test.body, test.handler)
			if response.Code != http.StatusAccepted {
				t.Fatalf("Git action returned %d: %s", response.Code, response.Body.String())
			}
			var accepted map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted["operation_id"] == "" {
				t.Fatalf("unexpected Git action response: %s", response.Body.String())
			}
			operation, err := database.Operation(ctx, accepted["operation_id"])
			if err != nil || operation.Kind != "git.workspace.write" || operation.WorkspaceID != workspaceID {
				t.Fatalf("unexpected Git write operation: %#v %v", operation, err)
			}
			var command protocol.GitWorkspaceWriteCommand
			if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
				t.Fatal(err)
			}
			if command.Action != test.wantAction || command.All != test.wantAll || command.IncludeStaged != test.wantIncludeStaged || command.Message != test.wantMessage {
				t.Fatalf("unexpected Git command: %#v", command)
			}
			if test.wantPath != "" && (len(command.Paths) != 1 || command.Paths[0] != test.wantPath) {
				t.Fatalf("unexpected Git command paths: %#v", command.Paths)
			}
			if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operation.ID, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitBranchRouteSupportsSlashNames(t *testing.T) {
	router := chi.NewRouter()
	handler := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, gitBranchURLParam(r))
	}
	router.Patch("/workspaces/{workspaceID}/git/branches/{branch}", handler)
	router.Patch("/workspaces/{workspaceID}/git/branches/*", handler)

	for _, requestPath := range []string{
		"/workspaces/workspace-1/git/branches/feature%2Fproject-management",
		"/workspaces/workspace-1/git/branches/feature/project-management",
	} {
		request := httptest.NewRequest(http.MethodPatch, requestPath, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "feature/project-management" {
			t.Fatalf("branch route %q returned %d %q", requestPath, response.Code, response.Body.String())
		}
	}
}

func TestUpdateProjectRejectsInvalidInputAndMissingProject(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	for name, body := range map[string]any{
		"empty patch":    map[string]any{},
		"blank name":     map[string]any{"name": "  "},
		"oversized name": map[string]any{"name": strings.Repeat("项", projectNameLimit+1)},
	} {
		t.Run(name, func(t *testing.T) {
			response := projectResourceRequest(t, http.MethodPatch, "/api/projects/missing", "missing", body, api.updateProject)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	missing := projectResourceRequest(t, http.MethodPatch, "/api/projects/missing", "missing", map[string]any{"pinned": true}, api.updateProject)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing project returned %d: %s", missing.Code, missing.Body.String())
	}
}

func TestUpdateThreadReturnsPreferencesAndWritesAudit(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "update-thread-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/update-thread", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "before")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPatch, "/api/threads/"+thread.ID, thread.ID, map[string]any{"title": "  after  ", "pinned": true}, api.updateThread)
	if response.Code != http.StatusOK {
		t.Fatalf("update returned %d: %s", response.Code, response.Body.String())
	}
	var updated store.Thread
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "after" || updated.PinnedAt == nil || updated.ProjectPinnedAt != nil || updated.ProjectHiddenAt != nil {
		t.Fatalf("unexpected updated thread: %#v", updated)
	}
	var auditCount int
	if err := database.DB.GetContext(ctx, &auditCount, database.Q("SELECT COUNT(*) FROM audit_log WHERE action='codex.thread.update' AND resource_id=?"), updated.ID); err != nil || auditCount != 1 {
		t.Fatalf("thread audit missing: count=%d err=%v", auditCount, err)
	}
}

func TestUpdateThreadRejectsInvalidInputAndMissingThread(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	for name, body := range map[string]any{
		"empty patch":     map[string]any{},
		"blank title":     map[string]any{"title": "  "},
		"oversized title": map[string]any{"title": strings.Repeat("题", threadTitleLimit+1)},
	} {
		t.Run(name, func(t *testing.T) {
			response := threadResourceRequest(t, http.MethodPatch, "/api/threads/missing", "missing", body, api.updateThread)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
	missing := threadResourceRequest(t, http.MethodPatch, "/api/threads/missing", "missing", map[string]any{"pinned": true}, api.updateThread)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing thread returned %d: %s", missing.Code, missing.Body.String())
	}
}

func projectResourceRequest(t *testing.T, method, target, projectID string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("projectID", projectID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	return directJSONRequest(t, method, target, body, nil, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(requestContext))
	})
}

func threadResourceRequest(t *testing.T, method, target, threadID string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("threadID", threadID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	return directJSONRequest(t, method, target, body, nil, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(requestContext))
	})
}

func rewriteResourceRequest(t *testing.T, threadID, eventID string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("threadID", threadID)
	route.URLParams.Add("eventID", eventID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	return directJSONRequest(t, http.MethodPost, "/api/threads/"+threadID+"/events/"+eventID+"/rewrite", body, nil, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(requestContext))
	})
}

func workspaceResourceRequest(t *testing.T, method, target, workspaceID string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("workspaceID", workspaceID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	return directJSONRequest(t, method, target, body, nil, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(requestContext))
	})
}

func TestDeploymentTargetManagementAndLogs(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "deployment-management-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.28", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(context.Background(), "deploy-management", "https://example.com/deploy-management.git")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	created := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"project_id": project.ID, "server_id": server.ID, "environment": "production", "repository": project.RemoteURL, "public_url": "http://203.0.113.10:5000", "health_checks": []map[string]any{{"type": "http", "address": "https://example.com/health", "timeout_seconds": 60}}}, &store.Session{UserID: "test-user"}, api.createDeploymentTarget)
	if created.Code != http.StatusCreated {
		t.Fatalf("target creation returned %d: %s", created.Code, created.Body.String())
	}
	var target store.DeploymentTarget
	if err := json.Unmarshal(created.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	if target.PublicURL != "http://203.0.113.10:5000" {
		t.Fatalf("target public URL was not persisted: %#v", target)
	}
	bareURL := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"project_id": project.ID, "server_id": server.ID, "environment": "bare-url", "repository": project.RemoteURL, "public_url": "app.example.com"}, &store.Session{UserID: "test-user"}, api.createDeploymentTarget)
	if bareURL.Code != http.StatusCreated || !strings.Contains(bareURL.Body.String(), `"public_url":"http://app.example.com"`) {
		t.Fatalf("bare public URL was not normalized: %d %s", bareURL.Code, bareURL.Body.String())
	}
	invalidURL := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"project_id": project.ID, "server_id": server.ID, "environment": "invalid", "repository": project.RemoteURL, "public_url": "ftp://example.com/service"}, &store.Session{UserID: "test-user"}, api.createDeploymentTarget)
	if invalidURL.Code != http.StatusBadRequest {
		t.Fatalf("invalid public URL returned %d: %s", invalidURL.Code, invalidURL.Body.String())
	}
	updated := deploymentResourceRequest(t, http.MethodPut, "/api/deployment-targets/"+target.ID, "targetID", target.ID, map[string]any{"project_id": project.ID, "server_id": server.ID, "environment": "staging", "repository": project.RemoteURL, "git_ref": "release", "compose_file": "deploy/compose.yaml", "build_mode": "pull", "public_url": "https://app.example.com", "health_checks": []map[string]any{}}, api.updateDeploymentTarget)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"environment":"staging"`) || !strings.Contains(updated.Body.String(), `"git_ref":"release"`) || !strings.Contains(updated.Body.String(), `"public_url":"https://app.example.com"`) {
		t.Fatalf("target update returned %d: %s", updated.Code, updated.Body.String())
	}
	deployment, err := database.CreateDeployment(context.Background(), target.ID, "release")
	if err != nil {
		t.Fatal(err)
	}
	emptyDetails := deploymentResourceRequest(t, http.MethodGet, "/api/deployments/"+deployment.ID, "deploymentID", deployment.ID, nil, api.deploymentDetails)
	if emptyDetails.Code != http.StatusOK || !strings.Contains(emptyDetails.Body.String(), `"events":[]`) || !strings.Contains(emptyDetails.Body.String(), `"public_url":"https://app.example.com"`) {
		t.Fatalf("empty deployment details returned %d: %s", emptyDetails.Code, emptyDetails.Body.String())
	}
	if err := database.SaveDeploymentStatus(context.Background(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "preparing", Message: "repository cloned", Content: "clone output"}); err != nil {
		t.Fatal(err)
	}
	details := deploymentResourceRequest(t, http.MethodGet, "/api/deployments/"+deployment.ID, "deploymentID", deployment.ID, nil, api.deploymentDetails)
	if details.Code != http.StatusOK || !strings.Contains(details.Body.String(), `"message":"repository cloned"`) || !strings.Contains(details.Body.String(), `"content":"clone output"`) {
		t.Fatalf("deployment details returned %d: %s", details.Code, details.Body.String())
	}
	activeDelete := deploymentResourceRequest(t, http.MethodDelete, "/api/deployments/"+deployment.ID, "deploymentID", deployment.ID, nil, api.deleteDeployment)
	if activeDelete.Code != http.StatusConflict {
		t.Fatalf("active deployment delete returned %d: %s", activeDelete.Code, activeDelete.Body.String())
	}
	if err := database.SaveDeploymentStatus(context.Background(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "compose failed"}); err != nil {
		t.Fatal(err)
	}
	deleted := deploymentResourceRequest(t, http.MethodDelete, "/api/deployments/"+deployment.ID, "deploymentID", deployment.ID, nil, api.deleteDeployment)
	if deleted.Code != http.StatusOK {
		t.Fatalf("deployment delete returned %d: %s", deleted.Code, deleted.Body.String())
	}
	targetDeleted := deploymentResourceRequest(t, http.MethodDelete, "/api/deployment-targets/"+target.ID, "targetID", target.ID, nil, api.deleteDeploymentTarget)
	if targetDeleted.Code != http.StatusAccepted {
		t.Fatalf("target delete returned %d: %s", targetDeleted.Code, targetDeleted.Body.String())
	}
	var deleteQueued struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(targetDeleted.Body.Bytes(), &deleteQueued); err != nil || deleteQueued.OperationID == "" {
		t.Fatalf("unexpected target deletion response: %#v %v", deleteQueued, err)
	}
	deleteOperation, err := database.Operation(context.Background(), deleteQueued.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var deleteCommand protocol.ContainerActionCommand
	if err := api.vault.Decrypt(deleteOperation.Payload, &deleteCommand); err != nil || deleteCommand.Action != "delete" || deleteCommand.TargetID != target.ID {
		t.Fatalf("unexpected target deletion command: %#v %v", deleteCommand, err)
	}
	deleteData, _ := json.Marshal(protocol.ContainerActionResult{TargetID: target.ID, Action: "delete", State: "removed", Message: "deployment files deleted"})
	if err := database.CompleteDeploymentContainerOperation(context.Background(), deleteQueued.OperationID, protocol.OperationResult{OperationID: deleteQueued.OperationID, Status: "succeeded", Data: deleteData}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DeploymentTarget(context.Background(), target.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("target remained after Agent cleanup: %v", err)
	}
}

func TestDeploymentTargetUsesServerWorkspaceAndRemoteRepositorySources(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "deployment-source-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.28", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/existing", Name: "existing", RemoteURL: "https://example.com/existing.git", Branch: "develop"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	api := resourceTestAPI(database)
	workspaceResponse := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"source_type": "workspace", "workspace_id": workspaces[0].ID, "server_id": server.ID, "environment": "staging", "compose_file": "compose.yaml"}, &store.Session{UserID: "test-user"}, api.createDeploymentTarget)
	if workspaceResponse.Code != http.StatusCreated || !strings.Contains(workspaceResponse.Body.String(), `"source_type":"workspace"`) || !strings.Contains(workspaceResponse.Body.String(), `"workspace_path":"/srv/existing"`) || !strings.Contains(workspaceResponse.Body.String(), `"git_ref":"develop"`) {
		t.Fatalf("workspace target returned %d: %s", workspaceResponse.Code, workspaceResponse.Body.String())
	}
	remoteResponse := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"source_type": "remote", "server_id": server.ID, "environment": "production", "repository": "https://example.com/new-service.git"}, &store.Session{UserID: "test-user"}, api.createDeploymentTarget)
	if remoteResponse.Code != http.StatusCreated || !strings.Contains(remoteResponse.Body.String(), `"source_type":"remote"`) || !strings.Contains(remoteResponse.Body.String(), `"project_name":"new-service"`) {
		t.Fatalf("remote target returned %d: %s", remoteResponse.Code, remoteResponse.Body.String())
	}
}

func TestStartDeploymentCarriesConfiguredAndFallbackPublicAddressInputs(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "deployment-public-address-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.42", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := database.UpdateServerMetadata(ctx, server.ID, store.ServerMetadata{Address: "203.0.113.10"}); err != nil || !ok {
		t.Fatalf("could not set server address: %v", err)
	}
	project, err := database.CreateProject(ctx, "public-address-command", "https://example.com/public-address-command.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL, PublicURL: "http://203.0.113.10:5010"})
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/"+target.ID+"/deploy", "targetID", target.ID, map[string]string{"commit_ref": "main"}, api.startDeployment)
	if response.Code != http.StatusAccepted {
		t.Fatalf("deployment start returned %d: %s", response.Code, response.Body.String())
	}
	var queued struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued.OperationID == "" {
		t.Fatalf("unexpected deployment response: %#v %v", queued, err)
	}
	operation, err := database.Operation(ctx, queued.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.DeployCommand
	if err := api.vault.Decrypt(operation.Payload, &command); err != nil {
		t.Fatal(err)
	}
	if command.ServerAddress != "203.0.113.10" || command.PublicURL != "http://203.0.113.10:5010" {
		t.Fatalf("deployment command omitted public address inputs: %#v", command)
	}
}

func TestDeploymentContainerActionsQueueEncryptedOperationsAndExposeState(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "deployment-container-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.28", ScanRoots: []string{"/srv"}}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "container-actions", "https://example.com/container-actions.git")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateDeploymentTarget(ctx, store.DeploymentTarget{ProjectID: project.ID, ServerID: server.ID, Environment: "production", Repository: project.RemoteURL})
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/"+target.ID+"/container", "targetID", target.ID, map[string]string{"action": "stop"}, api.deploymentContainerAction)
	if response.Code != http.StatusAccepted {
		t.Fatalf("container stop returned %d: %s", response.Code, response.Body.String())
	}
	var queued struct {
		OperationID string `json:"operation_id"`
		Action      string `json:"action"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued.OperationID == "" || queued.Action != "stop" {
		t.Fatalf("unexpected container response: %#v %v", queued, err)
	}
	operation, err := database.Operation(ctx, queued.OperationID)
	if err != nil || operation.Kind != "deploy.container" || !strings.HasPrefix(operation.Payload, "v1:") {
		t.Fatalf("container operation was not encrypted: %#v %v", operation, err)
	}
	target, err = database.DeploymentTarget(ctx, target.ID)
	if err != nil || target.ContainerStatus != "pending" || target.ContainerAction != "stop" {
		t.Fatalf("container target was not marked pending: %#v %v", target, err)
	}
	conflict := deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/"+target.ID+"/container", "targetID", target.ID, map[string]string{"action": "restart"}, api.deploymentContainerAction)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("parallel container action returned %d: %s", conflict.Code, conflict.Body.String())
	}
	data, _ := json.Marshal(protocol.ContainerActionResult{TargetID: target.ID, Action: "stop", State: "stopped", Message: "Docker Compose project stopped"})
	result := protocol.OperationResult{OperationID: queued.OperationID, Status: "succeeded", Data: data}
	if err := database.CompleteDeploymentContainerOperation(ctx, queued.OperationID, result); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(ctx, result); err != nil {
		t.Fatal(err)
	}
	listed := directJSONRequest(t, http.MethodGet, "/api/deployment-targets", nil, &store.Session{UserID: "test-user"}, api.deploymentTargets)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"container_status":"stopped"`) || !strings.Contains(listed.Body.String(), `"container_message":"Docker Compose project stopped"`) {
		t.Fatalf("container state was not exposed: %d %s", listed.Code, listed.Body.String())
	}
}

func deploymentResourceRequest(t *testing.T, method, target, param, id string, body any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add(param, id)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	return directJSONRequest(t, method, target, body, nil, func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.WithContext(requestContext))
	})
}

func enrollResourceTestServer(t *testing.T, database *store.Store, token string) store.Server {
	t.Helper()
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "node-1", []string{"/srv"}, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "node-1.local", token+"-agent")
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestCodexGoalRefreshQueuesFixedOperation(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "codex-goal-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.9", CodexVersion: "codex-cli 0.144.5", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "Goal test")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	route := chi.NewRouteContext()
	route.URLParams.Add("threadID", thread.ID)
	requestContext := context.WithValue(context.Background(), chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	response := directJSONRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/goal/refresh", map[string]any{}, nil, func(w http.ResponseWriter, r *http.Request) { api.refreshThreadGoal(w, r.WithContext(requestContext)) })
	if response.Code != http.StatusAccepted {
		t.Fatalf("refresh returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.goal.get" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.CodexSnapshotCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.ScopeID != thread.ID || command.CodexVersion != "codex-cli 0.144.5" {
		t.Fatalf("unexpected command: %#v %v", command, err)
	}
	snapshot, err := database.CodexSnapshot(ctx, "thread", thread.ID, "goal")
	if err != nil || snapshot.Status != "loading" {
		t.Fatalf("unexpected snapshot: %#v %v", snapshot, err)
	}
}

func TestCodexCompactQueuesThreadOperation(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "codex-compact-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.9", CodexVersion: "codex-cli 0.145.0", CodexReady: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "Compact test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "codex-compact", thread.ID); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/compact", thread.ID, map[string]any{}, api.compactThread)
	if response.Code != http.StatusAccepted {
		t.Fatalf("compact returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "codex.thread.compact" {
		t.Fatalf("unexpected operations: %#v %v", operations, err)
	}
	var command protocol.CodexSnapshotCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || command.ThreadID != thread.ID || command.CodexThread != "codex-compact" {
		t.Fatalf("unexpected compact command: %#v %v", command, err)
	}
}

func TestForkRequiresOnlineServerAndArchivedThreadsAreReadOnly(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "fork-guard-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "Guarded")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "codex-guarded", thread.ID); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/fork", thread.ID, map[string]any{}, api.forkThread)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "offline") {
		t.Fatalf("offline fork returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 0 {
		t.Fatalf("offline fork queued work: %#v %v", operations, err)
	}
	archived := true
	if _, err := database.UpdateThread(ctx, thread.ID, nil, nil, &archived); err != nil {
		t.Fatal(err)
	}
	response = threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/turns", thread.ID, map[string]any{"prompt": "hello"}, api.startTurn)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "read-only") {
		t.Fatalf("archived turn returned %d: %s", response.Code, response.Body.String())
	}
	response = threadResourceRequest(t, http.MethodDelete, "/api/threads/"+thread.ID+"/goal", thread.ID, map[string]any{}, api.clearThreadGoal)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "read-only") {
		t.Fatalf("archived goal clear returned %d: %s", response.Code, response.Body.String())
	}
}

func resourceTestAPI(database *store.Store) *API {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := security.DevVault()
	hub := realtime.New()
	return &API{store: database, hub: hub, gateway: agentgateway.New(database, hub, vault, log), vault: vault, log: log}
}
