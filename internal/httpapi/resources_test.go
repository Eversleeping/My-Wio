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

func resourceTestAPI(database *store.Store) *API {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := security.DevVault()
	hub := realtime.New()
	return &API{store: database, hub: hub, gateway: agentgateway.New(database, hub, vault, log), vault: vault, log: log}
}
