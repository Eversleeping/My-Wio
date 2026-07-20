package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/gitprovider"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

type mockProjectRemoteCreator struct {
	result  gitprovider.Result
	err     error
	request gitprovider.Request
}

func (m *mockProjectRemoteCreator) Create(_ context.Context, request gitprovider.Request) (gitprovider.Result, error) {
	m.request = request
	return m.result, m.err
}

func TestCreateBlankProjectWithExistingRemoteQueuesOrigin(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blank-existing-remote-token")
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{Hostname: "blank-existing", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := directJSONRequest(t, http.MethodPost, "/api/projects", map[string]any{
		"mode": "blank", "name": "existing-remote", "server_id": server.ID,
		"remote": map[string]string{"mode": "existing", "url": "https://gitee.com/team/existing.git"},
	}, &store.Session{UserID: "test-user"}, api.createProject)
	if response.Code != http.StatusAccepted {
		t.Fatalf("existing remote returned %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Project     store.Project `json:"project"`
		OperationID string        `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(context.Background(), body.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil || command.RemoteURL != "https://gitee.com/team/existing.git" {
		t.Fatalf("remote URL was not queued: %#v %v", command, err)
	}
	remote, err := database.ProjectRemote(context.Background(), body.Project.ID, "origin")
	if err != nil || remote.Mode != "existing" || remote.FetchURL != command.RemoteURL || remote.Status != "ready" {
		t.Fatalf("unexpected project remote: %#v %v", remote, err)
	}
}

func TestCreateBlankProjectCreatesRemoteWithoutPersistingToken(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blank-provider-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "blank-provider", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	profile, err := database.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "git", Name: "provider", Endpoint: "https://gitee.com", Username: "user"}, "")
	if err != nil {
		t.Fatal(err)
	}
	vault := security.DevVault()
	ciphertext, err := vault.Encrypt("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	profile, err = database.SaveCredentialProfile(ctx, store.CredentialProfile{ID: profile.ID, Kind: "git", Name: "provider", Endpoint: "https://gitee.com", Username: "user"}, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO server_credential_profiles(server_id,git_profile_id,updated_at) VALUES(?,?,?)"), server.ID, profile.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	creator := &mockProjectRemoteCreator{result: gitprovider.Result{Provider: "gitee", Namespace: "team", Repository: "remote-project", FetchURL: "https://gitee.com/team/remote-project.git", PushURL: "https://gitee.com/team/remote-project.git", WebURL: "https://gitee.com/team/remote-project"}}
	api := resourceTestAPI(database)
	api.gitProviders = creator
	response := directJSONRequest(t, http.MethodPost, "/api/projects", map[string]any{
		"mode": "blank", "name": "remote-project", "server_id": server.ID,
		"remote": map[string]any{"mode": "create", "provider": "gitee", "namespace": "team", "repository": "remote-project", "visibility": "private"},
	}, &store.Session{UserID: "test-user"}, api.createProject)
	if response.Code != http.StatusAccepted {
		t.Fatalf("provider remote returned %d: %s", response.Code, response.Body.String())
	}
	if creator.request.Token != "provider-secret" || strings.Contains(response.Body.String(), "provider-secret") {
		t.Fatalf("provider secret escaped or was not delivered only to provider: request=%#v response=%s", creator.request, response.Body.String())
	}
	var body struct {
		Project     store.Project `json:"project"`
		OperationID string        `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, body.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "queued" || strings.Contains(operation.Payload, "provider-secret") {
		t.Fatalf("provider secret leaked to operation: %#v", operation)
	}
	remote, err := database.ProjectRemote(ctx, body.Project.ID, "origin")
	if err != nil || remote.Status != "ready" || remote.FetchURL != creator.result.FetchURL {
		t.Fatalf("unexpected created remote: %#v %v", remote, err)
	}
}

func TestCreateBlankProjectRemoteFailureIsRetryable(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blank-provider-failure-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "blank-provider-failure", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	vault := security.DevVault()
	ciphertext, err := vault.Encrypt("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := database.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "git", Name: "provider", Endpoint: "https://gitee.com", Username: "user"}, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO server_credential_profiles(server_id,git_profile_id,updated_at) VALUES(?,?,?)"), server.ID, profile.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	api.gitProviders = &mockProjectRemoteCreator{err: errors.New("provider unavailable")}
	response := directJSONRequest(t, http.MethodPost, "/api/projects", map[string]any{
		"mode": "blank", "name": "provider-failure", "server_id": server.ID,
		"remote": map[string]any{"mode": "create", "provider": "gitee", "repository": "provider-failure", "visibility": "private"},
	}, &store.Session{UserID: "test-user"}, api.createProject)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("provider failure returned %d: %s", response.Code, response.Body.String())
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Status != "failed" {
		t.Fatalf("failed project was not retained: %#v %v", projects, err)
	}
	operations, err := database.ListProjectOperations(ctx, projects[0].ID, 10)
	if err != nil || len(operations) != 1 || operations[0].Status != "failed" || strings.Contains(operations[0].Payload, "provider-secret") {
		t.Fatalf("unexpected failed operation: %#v %v", operations, err)
	}
}

func TestCreateBlankProjectQueuesProvisioningOperation(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blank-http-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "blank-http", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := directJSONRequest(t, http.MethodPost, "/api/projects", map[string]any{
		"mode": "blank", "name": "  Empty API  ", "server_id": server.ID,
		"destination": "apps/empty-api", "initial_branch": "trunk", "initialize_readme": false,
		"remote": map[string]string{"mode": "none"},
	}, &store.Session{UserID: "test-user"}, api.createProject)
	if response.Code != http.StatusAccepted {
		t.Fatalf("blank project returned %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Project     store.Project `json:"project"`
		WorkspaceID string        `json:"workspace_id"`
		OperationID string        `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Project.Name != "Empty API" || body.Project.Status != "provisioning" || body.Project.DefaultBranch != "trunk" || body.WorkspaceID == "" || body.OperationID == "" {
		t.Fatalf("unexpected blank project response: %#v", body)
	}
	operation, err := database.Operation(ctx, body.OperationID)
	if err != nil || operation.Kind != "git.project.create" || operation.ProjectID != body.Project.ID || operation.Status != "queued" {
		t.Fatalf("unexpected blank operation: %#v %v", operation, err)
	}
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil || command.WorkspaceID != body.WorkspaceID || command.Destination != "apps/empty-api" || command.InitialBranch != "trunk" {
		t.Fatalf("unexpected command: %#v %v", command, err)
	}
	var auditCount int
	if err := database.DB.GetContext(ctx, &auditCount, database.Q("SELECT COUNT(*) FROM audit_log WHERE action='project.create' AND resource_id=?"), body.Project.ID); err != nil || auditCount != 1 {
		t.Fatalf("project creation was not audited: %d %v", auditCount, err)
	}
}

func TestCreateBlankProjectValidatesServerInputsAndCommitIdentity(t *testing.T) {
	database := openBootstrapTestStore(t)
	offline := enrollResourceTestServer(t, database, "blank-offline-token")
	online := enrollResourceTestServer(t, database, "blank-online-token")
	if err := database.Heartbeat(context.Background(), online.ID, protocol.Heartbeat{Hostname: "blank-online", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	valid := map[string]any{"mode": "blank", "name": "project", "server_id": online.ID, "initial_branch": "main", "initialize_readme": false, "remote": map[string]string{"mode": "none"}}
	for name, test := range map[string]struct {
		body map[string]any
		want int
	}{
		"missing server":       {body: mergeProjectInput(valid, map[string]any{"server_id": "missing"}), want: http.StatusNotFound},
		"offline server":       {body: mergeProjectInput(valid, map[string]any{"server_id": offline.ID}), want: http.StatusConflict},
		"missing name":         {body: mergeProjectInput(valid, map[string]any{"name": " "}), want: http.StatusBadRequest},
		"invalid branch":       {body: mergeProjectInput(valid, map[string]any{"initial_branch": "bad branch"}), want: http.StatusBadRequest},
		"invalid destination":  {body: mergeProjectInput(valid, map[string]any{"destination": "bad\npath"}), want: http.StatusBadRequest},
		"unsupported remote":   {body: mergeProjectInput(valid, map[string]any{"remote": map[string]string{"mode": "existing"}}), want: http.StatusBadRequest},
		"missing Git identity": {body: mergeProjectInput(valid, map[string]any{"initialize_readme": true}), want: http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			response := directJSONRequest(t, http.MethodPost, "/api/projects", test.body, &store.Session{UserID: "test-user"}, api.createProject)
			if response.Code != test.want {
				t.Fatalf("returned %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
	projects, err := database.ListProjects(context.Background())
	if err != nil || len(projects) != 0 {
		t.Fatalf("invalid requests created projects: %#v %v", projects, err)
	}
}

func TestCreateBlankProjectAllowsInitialCommitWithGitProfile(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blank-readme-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "blank-readme", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	profile, err := database.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "git", Name: "Git identity", Endpoint: "https://gitee.com", Username: "git-user", CommitName: "Wio User", CommitEmail: "wio@example.com"}, "v1:test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO server_credential_profiles(server_id,git_profile_id,updated_at) VALUES(?,?,?)"), server.ID, profile.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := directJSONRequest(t, http.MethodPost, "/api/projects", map[string]any{
		"mode": "blank", "name": "readme-project", "server_id": server.ID,
		"initial_branch": "main", "initialize_readme": true, "remote": map[string]string{"mode": "none"},
	}, &store.Session{UserID: "test-user"}, api.createProject)
	if response.Code != http.StatusAccepted {
		t.Fatalf("README project returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 {
		t.Fatalf("unexpected README operation: %#v %v", operations, err)
	}
	var command protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil || !command.InitializeREADME {
		t.Fatalf("README option was not queued: %#v %v", command, err)
	}
}

func TestRetryBlankProjectRequiresOnlineServerAndPreservesCommand(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blank-retry-http-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "blank-retry", AgentVersion: "0.2.23"}); err != nil {
		t.Fatal(err)
	}
	provision, err := database.CreateBlankProject(ctx, server.ID, "retry-http", "apps/retry-http", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := database.Operation(ctx, provision.OperationID)
	if err := database.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: "temporary failure"}, "failed"); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	response := projectResourceRequest(t, http.MethodPost, "/api/projects/"+provision.Project.ID+"/retry-create", provision.Project.ID, map[string]any{}, api.retryProjectCreation)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry returned %d: %s", response.Code, response.Body.String())
	}
	operations, err := database.ListProjectOperations(ctx, provision.Project.ID, 10)
	if err != nil || len(operations) != 2 || operations[0].ID == operation.ID || operations[0].Status != "queued" {
		t.Fatalf("unexpected retry operations: %#v %v", operations, err)
	}
	var retryCommand protocol.GitProjectCreateCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &retryCommand); err != nil || retryCommand.WorkspaceID != provision.WorkspaceID || retryCommand.Destination != provision.Command.Destination {
		t.Fatalf("retry changed command identity: %#v %v", retryCommand, err)
	}
	if err := database.FailBlankProject(ctx, operations[0], retryCommand, protocol.OperationResult{OperationID: operations[0].ID, Status: "failed", Message: "again"}, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-10*time.Minute), server.ID); err != nil {
		t.Fatal(err)
	}
	offline := projectResourceRequest(t, http.MethodPost, "/api/projects/"+provision.Project.ID+"/retry-create", provision.Project.ID, map[string]any{}, api.retryProjectCreation)
	if offline.Code != http.StatusConflict || !strings.Contains(offline.Body.String(), "offline") {
		t.Fatalf("offline retry returned %d: %s", offline.Code, offline.Body.String())
	}
	after, err := database.ListProjectOperations(ctx, provision.Project.ID, 10)
	if err != nil || len(after) != 2 {
		t.Fatalf("offline retry queued an operation: %#v %v", after, err)
	}
}

func mergeProjectInput(base map[string]any, overrides map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(overrides))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range overrides {
		result[key] = value
	}
	return result
}
