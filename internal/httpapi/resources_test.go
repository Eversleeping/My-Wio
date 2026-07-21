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
	if emptySnapshot.Data.Branches == nil || emptySnapshot.Data.Remotes == nil || emptySnapshot.Data.Commits == nil {
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
	if emptySnapshot.Data.Branches == nil || emptySnapshot.Data.Remotes == nil || emptySnapshot.Data.Commits == nil {
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
	project, err := database.CreateProject(context.Background(), "deploy-management", "https://example.com/deploy-management.git")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	created := directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"project_id": project.ID, "server_id": server.ID, "environment": "production", "repository": project.RemoteURL, "health_checks": []map[string]any{{"type": "http", "address": "https://example.com/health", "timeout_seconds": 60}}}, &store.Session{UserID: "test-user"}, api.createDeploymentTarget)
	if created.Code != http.StatusCreated {
		t.Fatalf("target creation returned %d: %s", created.Code, created.Body.String())
	}
	var target store.DeploymentTarget
	if err := json.Unmarshal(created.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	updated := deploymentResourceRequest(t, http.MethodPut, "/api/deployment-targets/"+target.ID, "targetID", target.ID, map[string]any{"project_id": project.ID, "server_id": server.ID, "environment": "staging", "repository": project.RemoteURL, "git_ref": "release", "compose_file": "deploy/compose.yaml", "build_mode": "pull", "health_checks": []map[string]any{}}, api.updateDeploymentTarget)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"environment":"staging"`) || !strings.Contains(updated.Body.String(), `"git_ref":"release"`) {
		t.Fatalf("target update returned %d: %s", updated.Code, updated.Body.String())
	}
	deployment, err := database.CreateDeployment(context.Background(), target.ID, "release")
	if err != nil {
		t.Fatal(err)
	}
	emptyDetails := deploymentResourceRequest(t, http.MethodGet, "/api/deployments/"+deployment.ID, "deploymentID", deployment.ID, nil, api.deploymentDetails)
	if emptyDetails.Code != http.StatusOK || !strings.Contains(emptyDetails.Body.String(), `"events":[]`) {
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
	if targetDeleted.Code != http.StatusOK {
		t.Fatalf("target delete returned %d: %s", targetDeleted.Code, targetDeleted.Body.String())
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
