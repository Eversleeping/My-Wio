package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestOpenMigratesLegacyResourceManagementSchemaIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-resources.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE servers (id TEXT PRIMARY KEY,name TEXT NOT NULL);
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,name TEXT NOT NULL,remote_url TEXT NOT NULL DEFAULT '',normalized_remote TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,path TEXT NOT NULL,
			branch TEXT NOT NULL DEFAULT '',commit_sha TEXT NOT NULL DEFAULT '',dirty INTEGER NOT NULL DEFAULT 0,
			last_scanned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(server_id,path)
		);
		CREATE TABLE agent_operations (
			id TEXT PRIMARY KEY,server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,kind TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',status TEXT NOT NULL DEFAULT 'queued',idempotency_key TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,delivered_at TIMESTAMP,completed_at TIMESTAMP,result TEXT
		);
		INSERT INTO servers(id,name) VALUES('legacy-server','Legacy Server');
		INSERT INTO projects(id,name) VALUES('legacy-project','Legacy Project');
		INSERT INTO workspaces(id,project_id,server_id,path,branch,commit_sha) VALUES('legacy-workspace','legacy-project','legacy-server','/srv/legacy','main','abc123');
		INSERT INTO agent_operations(id,server_id,kind,idempotency_key,result) VALUES('legacy-operation','legacy-server','inventory.scan','legacy-scan','done');
	`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	assertMigrated := func(database *Store) {
		t.Helper()
		ctx := context.Background()
		project, err := database.Project(ctx, "legacy-project")
		if err != nil {
			t.Fatal(err)
		}
		if project.Description != "" || project.DefaultBranch != "main" || project.Status != "ready" || project.ProvisionError != "" || project.ArchivedAt != nil {
			t.Fatalf("unexpected migrated project: %#v", project)
		}
		workspace, err := database.Workspace(ctx, "legacy-workspace")
		if err != nil {
			t.Fatal(err)
		}
		if workspace.DisplayName != "" || workspace.ManagementMode != "observed" || workspace.Status != "ready" || workspace.LastGitRefreshAt != nil || workspace.GitError != "" {
			t.Fatalf("unexpected migrated workspace: %#v", workspace)
		}
		operation, err := database.Operation(ctx, "legacy-operation")
		if err != nil {
			t.Fatal(err)
		}
		if operation.ProjectID != "" || operation.WorkspaceID != "" || operation.WorkspaceWrite != 0 || operation.Status != "queued" || operation.Result != "done" || operation.ResultData != "{}" {
			t.Fatalf("unexpected migrated operation: %#v", operation)
		}
		for table, expected := range map[string][]string{
			"projects":         {"description", "default_branch", "status", "provision_error", "archived_at"},
			"workspaces":       {"display_name", "management_mode", "status", "last_git_refresh_at", "git_error"},
			"agent_operations": {"project_id", "workspace_id", "workspace_write", "result_data"},
		} {
			var columns []string
			if err := database.DB.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info(?)", table); err != nil {
				t.Fatal(err)
			}
			for _, column := range expected {
				if !containsString(columns, column) {
					t.Fatalf("%s migration missing %s: %v", table, column, columns)
				}
			}
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		database, err := Open(path + "?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt+1, err)
		}
		assertMigrated(database)
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenNormalizesLegacyWorkspaceLifecycleStatuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-workspace-status.db")
	database, err := Open(path + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	server := createOperationTestServer(t, database, "legacy-status", "legacy-status-token")
	if err := database.UpsertInventory(context.Background(), server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/legacy-status", Name: "legacy-status"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(context.Background())
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspaceID := workspaces[0].ID
	if _, err := database.DB.Exec(database.Q("UPDATE workspaces SET status='moveing' WHERE id=?"), workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	for _, status := range []struct{ legacy, expected string }{{"moveing", "moving"}, {"deleteing", "deleting"}} {
		database, err = Open(path + "?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		workspace, err := database.Workspace(context.Background(), workspaceID)
		if err != nil || workspace.Status != status.expected {
			t.Fatalf("legacy status %q was not normalized to %q: %#v %v", status.legacy, status.expected, workspace, err)
		}
		if status.expected == "moving" {
			if _, err := database.DB.Exec(database.Q("UPDATE workspaces SET status='deleteing' WHERE id=?"), workspaceID); err != nil {
				t.Fatal(err)
			}
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRestoresManagedModeForHistoricalWorkspaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-worktree.db")
	database, err := Open(path + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	server := createOperationTestServer(t, database, "worktree-owner", "worktree-owner-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "worktree-owner", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(context.Background(), "owned-worktree", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO workspaces(id,project_id,server_id,path,kind) VALUES(?,?,?,?,'primary')"), "parent", project.ID, server.ID, "/srv/external/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO workspaces(id,project_id,server_id,path,management_mode,kind,parent_workspace_id) VALUES(?,?,?,?,?,'worktree',?)"), "owned", project.ID, server.ID, "/srv/external/repo-feature", "observed", "parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO workspaces(id,project_id,server_id,path,management_mode,kind) VALUES(?,?,?,?,?,'primary')"), "managed-path", project.ID, server.ID, "/srv/managed/project", "observed"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO workspaces(id,project_id,server_id,path,management_mode,kind) VALUES(?,?,?,?,?,'worktree')"), "unowned", project.ID, server.ID, "/srv/external/unowned-worktree", "observed"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		database, err = Open(path + "?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt+1, err)
		}
		owned, ownedErr := database.Workspace(ctx, "owned")
		managedPath, managedPathErr := database.Workspace(ctx, "managed-path")
		unowned, unownedErr := database.Workspace(ctx, "unowned")
		if ownedErr != nil || owned.ManagementMode != "managed" {
			t.Fatalf("owned historical worktree was not restored: %#v %v", owned, ownedErr)
		}
		plan, planErr := database.WorkspaceDeletionPlan(ctx, owned.ID, false)
		if planErr != nil || !plan.Managed || !plan.CanDeleteFiles {
			t.Fatalf("restored worktree must allow managed-file deletion: %#v %v", plan, planErr)
		}
		if managedPathErr != nil || managedPath.ManagementMode != "managed" {
			t.Fatalf("workspace below a managed root was not restored: %#v %v", managedPath, managedPathErr)
		}
		if unownedErr != nil || unowned.ManagementMode != "observed" {
			t.Fatalf("unowned workspace must remain observed: %#v %v", unowned, unownedErr)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpsertInventoryClassifiesManagedWorkspacePaths(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "managed-inventory", "managed-inventory-token")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "managed-inventory", ManagedRoots: []string{"/srv/managed"}}); err != nil {
		t.Fatal(err)
	}
	repositories := []protocol.Repository{
		{Path: "/srv/managed/app", Name: "managed"},
		{Path: "/srv/managed-other/app", Name: "prefix"},
		{Path: "/srv/external/app", Name: "external"},
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: repositories}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modes := make(map[string]string, len(workspaces))
	for _, workspace := range workspaces {
		modes[workspace.Path] = workspace.ManagementMode
	}
	if modes["/srv/managed/app"] != "managed" || modes["/srv/managed-other/app"] != "observed" || modes["/srv/external/app"] != "observed" {
		t.Fatalf("unexpected inventory management modes: %#v", modes)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='observed' WHERE server_id=? AND path=?"), server.ID, "/srv/managed/app"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: repositories[:1]}); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := database.DB.GetContext(ctx, &mode, database.Q("SELECT management_mode FROM workspaces WHERE server_id=? AND path=?"), server.ID, "/srv/managed/app"); err != nil || mode != "managed" {
		t.Fatalf("inventory did not promote the managed workspace: %q %v", mode, err)
	}
}

func TestResourceOperationsPersistResultsAndSupportQueries(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "resource-server", "resource-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/app", Name: "app", Branch: "main", CommitSHA: "abc123"}}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("unexpected projects: %#v %v", projects, err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	project, workspace := projects[0], workspaces[0]
	if workspace.DisplayName != "app" || workspace.ManagementMode != "observed" || workspace.LastGitRefreshAt == nil {
		t.Fatalf("inventory did not initialize workspace metadata: %#v", workspace)
	}

	operationID, err := database.QueueResourceOperation(ctx, server.ID, "git.branch.create", map[string]string{"branch": "feature/test"}, "resource-write", OperationResource{WorkspaceID: workspace.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	duplicateID, err := database.QueueResourceOperation(ctx, server.ID, "git.branch.create", map[string]string{"branch": "feature/test"}, "resource-write", OperationResource{WorkspaceID: workspace.ID}, true)
	if err != nil || duplicateID != operationID {
		t.Fatalf("resource operation idempotency failed: %q %q %v", operationID, duplicateID, err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.ProjectID != project.ID || operation.WorkspaceID != workspace.ID || operation.Status != "queued" || operation.WorkspaceWrite != 1 || operation.ResultData != "{}" {
		t.Fatalf("resource association was not returned: %#v", operation)
	}
	active, err := database.HasActiveWorkspaceWriteOperation(ctx, workspace.ID)
	if err != nil || !active {
		t.Fatalf("queued write operation should be active: %v %v", active, err)
	}
	if _, err := database.QueueResourceOperation(ctx, server.ID, "git.checkout", struct{}{}, "second-resource-write", OperationResource{WorkspaceID: workspace.ID}, true); !errors.Is(err, ErrWorkspaceWriteActive) {
		t.Fatalf("second active write should be rejected atomically: %v", err)
	}
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	active, err = database.HasActiveWorkspaceWriteOperation(ctx, workspace.ID)
	if err != nil || !active {
		t.Fatalf("delivered write operation should be active: %v %v", active, err)
	}
	resultData := json.RawMessage(`{"branch":"feature/test","commit":"def456"}`)
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Message: "created", Data: resultData}); err != nil {
		t.Fatal(err)
	}
	operation, err = database.Operation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Status != "succeeded" || operation.Result != "created" || operation.ResultData != string(resultData) || operation.DeliveredAt == nil || operation.CompletedAt == nil {
		t.Fatalf("operation completion fields missing: %#v", operation)
	}
	active, err = database.HasActiveWorkspaceWriteOperation(ctx, workspace.ID)
	if err != nil || active {
		t.Fatalf("completed write operation should not be active: %v %v", active, err)
	}

	readID, err := database.QueueResourceOperation(ctx, server.ID, "git.status", struct{}{}, "resource-read", OperationResource{ProjectID: project.ID, WorkspaceID: workspace.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	active, err = database.HasActiveWorkspaceWriteOperation(ctx, workspace.ID)
	if err != nil || active {
		t.Fatalf("read-only operation should not take the write lock: %v %v", active, err)
	}
	projectOnlyID, err := database.QueueResourceOperation(ctx, server.ID, "git.remote.create", struct{}{}, "project-write", OperationResource{ProjectID: project.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	projectOperations, err := database.ListProjectOperations(ctx, project.ID, 2)
	if err != nil || len(projectOperations) != 2 || projectOperations[0].ID != projectOnlyID || projectOperations[1].ID != readID {
		t.Fatalf("unexpected project operations: %#v %v", projectOperations, err)
	}
	workspaceOperations, err := database.ListWorkspaceOperations(ctx, workspace.ID, 0)
	if err != nil || len(workspaceOperations) != 2 || workspaceOperations[0].ID != readID || workspaceOperations[1].ID != operationID {
		t.Fatalf("unexpected workspace operations: %#v %v", workspaceOperations, err)
	}
}

func TestQueueResourceOperationValidatesOwnershipAndResultJSON(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "owner-server", "owner-token")
	otherServer := createOperationTestServer(t, database, "other-server", "other-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/owner", Name: "owner"}}}); err != nil {
		t.Fatal(err)
	}
	projects, _ := database.ListProjects(ctx)
	workspaces, _ := database.ListWorkspaces(ctx)
	otherProject, err := database.CreateProject(ctx, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	resource := OperationResource{ProjectID: projects[0].ID, WorkspaceID: workspaces[0].ID}
	if _, err := database.QueueResourceOperation(ctx, otherServer.ID, "git.checkout", struct{}{}, "wrong-server", resource, true); err == nil {
		t.Fatal("expected cross-server workspace association to fail")
	}
	resource.ProjectID = otherProject.ID
	if _, err := database.QueueResourceOperation(ctx, server.ID, "git.checkout", struct{}{}, "wrong-project", resource, true); err == nil {
		t.Fatal("expected cross-project workspace association to fail")
	}
	if _, err := database.QueueResourceOperation(ctx, server.ID, "git.checkout", struct{}{}, "missing-project", OperationResource{ProjectID: "missing"}, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project should return sql.ErrNoRows: %v", err)
	}

	if _, err := database.QueueResourceOperation(ctx, server.ID, "git.checkout", struct{}{}, "missing-write-workspace", OperationResource{ProjectID: projects[0].ID}, true); err == nil {
		t.Fatal("workspace write without workspace should fail")
	}
	operationID, err := database.QueueResourceOperation(ctx, server.ID, "git.checkout", struct{}{}, "invalid-result", OperationResource{WorkspaceID: workspaces[0].ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: json.RawMessage(`{`)}); err == nil {
		t.Fatal("invalid result JSON should be rejected")
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "queued" || operation.ResultData != "{}" {
		t.Fatalf("invalid completion should not mutate operation: %#v %v", operation, err)
	}
}

func TestWorkspaceWriteOperationsAreSerializedAtomically(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	server := createOperationTestServer(t, database, "serial-server", "serial-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/serial", Name: "serial"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}

	const candidates = 8
	start := make(chan struct{})
	errorsByCandidate := make([]error, candidates)
	var wait sync.WaitGroup
	for candidate := 0; candidate < candidates; candidate++ {
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, errorsByCandidate[candidate] = database.QueueResourceOperation(ctx, server.ID, "git.checkout", struct{}{}, "serial-write-"+string(rune('a'+candidate)), OperationResource{WorkspaceID: workspaces[0].ID}, true)
		}()
	}
	close(start)
	wait.Wait()

	succeeded, rejected := 0, 0
	for _, err := range errorsByCandidate {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrWorkspaceWriteActive):
			rejected++
		default:
			t.Fatalf("unexpected queue result: %v", err)
		}
	}
	if succeeded != 1 || rejected != candidates-1 {
		t.Fatalf("workspace write serialization failed: succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func createOperationTestServer(t *testing.T, database *Store, name, token string) Server {
	t.Helper()
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, name, []string{"/srv"}, token+"-enroll", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, token+"-enroll")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, name+".local", token+"-agent")
	if err != nil {
		t.Fatal(err)
	}
	return server
}
