package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sshbootstrap"
	"github.com/wio-platform/wio/internal/store"
)

func TestGitRemoteCredentialDetection(t *testing.T) {
	for _, remote := range []string{
		"https://user:token@github.com/org/repo.git",
		"https://token@github.com/org/repo.git",
		"HTTPS://user:token@github.com/org/repo.git",
		"http://user:token@github.com/org/repo.git",
		"ssh://git:secret@github.com/org/repo.git",
		"ssh://git:secret@github.com:org/repo.git",
		"https://user:to ken@github.com/org/repo.git",
		"https://user@token@github.com/org/repo.git",
	} {
		if err := gitRemoteCredentialError(remote); !errors.Is(err, errGitRemoteCredentials) {
			t.Errorf("%s: error = %v", remote, err)
		}
		if validGitRemote(remote) {
			t.Errorf("%s was accepted as a Git remote", remote)
		}
	}
	for _, remote := range []string{
		"https://github.com/org/repo.git",
		"https://github.com/@scope/repo.git",
		"ssh://git@github.com/org/repo.git",
		"git@github.com:org/repo.git",
	} {
		if err := gitRemoteCredentialError(remote); err != nil {
			t.Errorf("%s: unexpected error %v", remote, err)
		}
		if !validGitRemote(remote) {
			t.Errorf("%s was rejected as a Git remote", remote)
		}
	}
}

func TestEmbeddedGitCredentialsAreRejectedConsistently(t *testing.T) {
	const remote = "https://user:ghp_secret@github.com/org/repo.git"
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "git-credential-url-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "node-1", AgentVersion: "0.2.28"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	session := &store.Session{UserID: "test-user"}
	assertRejected := func(name string, response *httptest.ResponseRecorder) {
		t.Helper()
		body := response.Body.String()
		if response.Code != http.StatusBadRequest || !strings.Contains(body, "credentials vault") {
			t.Fatalf("%s returned %d: %s", name, response.Code, body)
		}
		if strings.Contains(body, "ghp_secret") {
			t.Fatalf("%s echoed the credential: %s", name, body)
		}
	}
	assertRejected("import", directJSONRequest(t, http.MethodPost, "/api/projects/import", map[string]string{"server_id": server.ID, "remote_url": remote}, session, api.importProject))
	assertRejected("deployment target", directJSONRequest(t, http.MethodPost, "/api/deployment-targets", map[string]any{"source_type": "remote", "server_id": server.ID, "environment": "production", "repository": remote}, session, api.createDeploymentTarget))
	assertRejected("git remote add", workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/missing/git/remotes", "missing", map[string]string{"name": "origin", "url": remote}, api.addGitRemote))
	assertRejected("git remote set-url", workspaceResourceRequest(t, http.MethodPatch, "/api/workspaces/missing/git/remotes/origin", "missing", map[string]string{"url": remote}, api.setGitRemote))

	var blank createProjectInput
	blank.Remote.Mode, blank.Remote.URL = "existing", remote
	if err := validateBlankProjectRemote(blank); !errors.Is(err, errGitRemoteCredentials) {
		t.Fatalf("blank project remote error = %v", err)
	}
	projects, err := database.ListProjects(context.Background())
	if err != nil || len(projects) != 0 {
		t.Fatalf("rejected requests stored projects: %#v %v", projects, err)
	}
}

func managedGitWorkspaceFixture(t *testing.T, database *store.Store, token string) (store.Server, store.Workspace) {
	t.Helper()
	ctx := context.Background()
	server := enrollResourceTestServer(t, database, token)
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "node-1", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/source", Name: "source", Branch: "main"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspace := workspaces[0]
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspace.ID); err != nil {
		t.Fatal(err)
	}
	return server, workspace
}

func TestRejectedGitWriteLeavesWorkspaceGitStatusUntouched(t *testing.T) {
	database := openBootstrapTestStore(t)
	server, workspace := managedGitWorkspaceFixture(t, database, "git-write-lock-token")
	ctx := context.Background()
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspace.ID, protocol.GitWorkspaceInspectResult{WorkspaceID: workspace.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueResourceOperation(ctx, server.ID, "git.workspace.write", map[string]string{}, "existing-write", store.OperationResource{ProjectID: workspace.ProjectID, WorkspaceID: workspace.ID}, true); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/git/fetch", workspace.ID, map[string]string{"remote": "origin"}, api.fetchGit)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), store.ErrWorkspaceWriteActive.Error()) {
		t.Fatalf("locked workspace write returned %d: %s", response.Code, response.Body.String())
	}
	snapshot, err := database.WorkspaceGitSnapshot(ctx, workspace.ID)
	if err != nil || snapshot.Status != "succeeded" || snapshot.Error != "" {
		t.Fatalf("rejected request changed the Git snapshot: %#v %v", snapshot, err)
	}
	current, err := database.Workspace(ctx, workspace.ID)
	if err != nil || current.GitError != "" {
		t.Fatalf("rejected request recorded a Git error: %#v %v", current, err)
	}
}

func TestRestoreWorkspaceGitRefreshPutsBackPreviousState(t *testing.T) {
	database := openBootstrapTestStore(t)
	_, workspace := managedGitWorkspaceFixture(t, database, "git-restore-token")
	ctx := context.Background()
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspace.ID, protocol.GitWorkspaceInspectResult{WorkspaceID: workspace.ID}); err != nil {
		t.Fatal(err)
	}
	previous, err := database.WorkspaceGitSnapshot(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BeginWorkspaceGitRefresh(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.RestoreWorkspaceGitRefresh(ctx, previous); err != nil {
		t.Fatal(err)
	}
	restored, err := database.WorkspaceGitSnapshot(ctx, workspace.ID)
	if err != nil || restored.Status != "succeeded" || restored.Error != "" {
		t.Fatalf("snapshot was not restored: %#v %v", restored, err)
	}
	// A refresh that another operation already finished must not be undone.
	if err := database.BeginWorkspaceGitRefresh(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.FailWorkspaceGitRefresh(ctx, workspace.ID, "agent failure"); err != nil {
		t.Fatal(err)
	}
	if err := database.RestoreWorkspaceGitRefresh(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if current, err := database.WorkspaceGitSnapshot(ctx, workspace.ID); err != nil || current.Status != "failed" || current.Error != "agent failure" {
		t.Fatalf("completed refresh was overwritten: %#v %v", current, err)
	}
}

func TestGitWriteStillStartsRefreshWhenUnlocked(t *testing.T) {
	database := openBootstrapTestStore(t)
	server, workspace := managedGitWorkspaceFixture(t, database, "git-write-unlocked-token")
	ctx := context.Background()
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspace.ID, protocol.GitWorkspaceInspectResult{WorkspaceID: workspace.ID}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := workspaceResourceRequest(t, http.MethodPost, "/api/workspaces/"+workspace.ID+"/git/fetch", workspace.ID, map[string]string{"remote": "origin"}, api.fetchGit)
	if response.Code != http.StatusAccepted {
		t.Fatalf("workspace write returned %d: %s", response.Code, response.Body.String())
	}
	snapshot, err := database.WorkspaceGitSnapshot(ctx, workspace.ID)
	if err != nil || snapshot.Status != "refreshing" {
		t.Fatalf("accepted write did not start a refresh: %#v %v", snapshot, err)
	}
	if active, err := database.HasActiveWorkspaceWriteOperation(ctx, workspace.ID); err != nil || !active {
		t.Fatalf("write was not queued for %s: %v %v", server.ID, active, err)
	}
}

func TestHandlersSeparateMissingResourcesFromStoreFailures(t *testing.T) {
	cases := map[string]func(*testing.T, *API) *httptest.ResponseRecorder{
		"start turn": func(t *testing.T, api *API) *httptest.ResponseRecorder {
			return threadResourceRequest(t, http.MethodPost, "/api/threads/missing/turns", "missing", map[string]string{"prompt": "hello"}, api.startTurn)
		},
		"steer turn": func(t *testing.T, api *API) *httptest.ResponseRecorder {
			return threadResourceRequest(t, http.MethodPost, "/api/threads/missing/steer", "missing", map[string]string{"prompt": "hello"}, api.steerTurn)
		},
		"interrupt turn": func(t *testing.T, api *API) *httptest.ResponseRecorder {
			return threadResourceRequest(t, http.MethodPost, "/api/threads/missing/interrupt", "missing", nil, api.interruptTurn)
		},
		"start deployment": func(t *testing.T, api *API) *httptest.ResponseRecorder {
			return deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/missing/deploy", "targetID", "missing", map[string]string{}, api.startDeployment)
		},
		"rollback deployment": func(t *testing.T, api *API) *httptest.ResponseRecorder {
			return deploymentResourceRequest(t, http.MethodPost, "/api/deployment-targets/missing/rollback", "targetID", "missing", map[string]string{}, api.rollbackDeployment)
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			database := openBootstrapTestStore(t)
			api := resourceTestAPI(database)
			if response := call(t, api); response.Code != http.StatusNotFound {
				t.Fatalf("missing resource returned %d: %s", response.Code, response.Body.String())
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if response := call(t, api); response.Code != http.StatusInternalServerError {
				t.Fatalf("store failure returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCrossServerCloneRequiresRefreshWhenSnapshotIsMissing(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := resourceTestAPI(database)
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces/missing/copy", nil)
	command, blocker, err := api.crossServerCloneCommand(request, store.Workspace{ID: "missing"}, "/srv/managed/copy")
	if err != nil || !strings.Contains(blocker, "refresh Git status first") || command.WorkspaceID != "" {
		t.Fatalf("missing snapshot: command=%#v blocker=%q error=%v", command, blocker, err)
	}
}

func TestRepairOfUnknownServerReturnsNotFound(t *testing.T) {
	for name, route := range map[string]func(*API) http.HandlerFunc{
		"repair":        func(api *API) http.HandlerFunc { return api.repairServerSSH },
		"repair stream": func(api *API) http.HandlerFunc { return api.streamRepairServerSSH },
	} {
		t.Run(name, func(t *testing.T) {
			database := openBootstrapTestStore(t)
			fake := &fakeServerBootstrapper{installError: sshbootstrap.ErrInstallation}
			api := &API{store: database, bootstrapper: fake, publicURL: "https://wio.example.com", log: discardTestLog()}
			response := directServerJSONRequest(t, http.MethodPost, "/api/servers/missing/ssh/repair", "missing", bootstrapInput(), &store.Session{UserID: "test-user"}, route(api))
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("non-JSON response %q: %v", response.Body.String(), err)
			}
			if response.Code != http.StatusNotFound || body.Code != "server_not_found" {
				t.Fatalf("unknown server returned %d: %s", response.Code, response.Body.String())
			}
			if fake.installRequest.EnrollmentToken != "" {
				t.Fatal("installation ran for an unknown server")
			}
		})
	}
}

func TestRepairStoreFailureStaysInternalError(t *testing.T) {
	database := openBootstrapTestStore(t)
	api := &API{store: database, bootstrapper: &fakeServerBootstrapper{}, publicURL: "https://wio.example.com", log: discardTestLog()}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	response := directServerJSONRequest(t, http.MethodPost, "/api/servers/missing/ssh/repair", "missing", bootstrapInput(), &store.Session{UserID: "test-user"}, api.repairServerSSH)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "enrollment_store_failed") {
		t.Fatalf("store failure returned %d: %s", response.Code, response.Body.String())
	}
}
