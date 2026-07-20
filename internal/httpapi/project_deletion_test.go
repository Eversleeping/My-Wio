package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestProjectDeletionPlanAndMetadataOnlyDelete(t *testing.T) {
	ctx := context.Background()
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "delete-plan-api")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "delete-plan-api.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "delete-plan-api", "https://example.com/delete-plan-api.git")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertHTTPDeletionWorkspace(t, database, project.ID, server.ID, "/srv/delete-plan-api", "managed", 1)
	api := resourceTestAPI(database)

	response := projectResourceRequest(t, http.MethodPost, "/api/projects/"+project.ID+"/deletion-plan", project.ID, nil, api.projectDeletionPlan)
	if response.Code != http.StatusOK {
		t.Fatalf("deletion plan returned %d: %s", response.Code, response.Body.String())
	}
	var plan store.ProjectDeletionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.RemotePreserved || !plan.CanDeleteMetadata || plan.CanDeleteManagedFiles || plan.DirtyManagedWorkspaces != 1 {
		t.Fatalf("unexpected deletion plan: %#v", plan)
	}

	blocked := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, map[string]string{"mode": "managed-files"}, api.deleteProject)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "dirty-managed-workspaces") {
		t.Fatalf("dirty managed deletion returned %d: %s", blocked.Code, blocked.Body.String())
	}
	metadata := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, map[string]string{"mode": "metadata-only"}, api.deleteProject)
	if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), `"remote_deleted":false`) {
		t.Fatalf("metadata deletion returned %d: %s", metadata.Code, metadata.Body.String())
	}
	if _, err := database.Project(ctx, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project metadata still exists: %v", err)
	}
	var workspaceCount int
	if err := database.DB.GetContext(ctx, &workspaceCount, database.Q("SELECT COUNT(*) FROM workspaces WHERE id=?"), workspaceID); err != nil || workspaceCount != 0 {
		t.Fatalf("workspace metadata was not cascaded: %d %v", workspaceCount, err)
	}
	var auditCount int
	if err := database.DB.GetContext(ctx, &auditCount, database.Q("SELECT COUNT(*) FROM audit_log WHERE action='project.delete.metadata_only' AND resource_id=? AND detail LIKE '%remote_preserved%'"), project.ID); err != nil || auditCount != 1 {
		t.Fatalf("metadata deletion was not audited: %d %v", auditCount, err)
	}
}

func TestManagedFilesDeleteQueuesStructuredAgentOperation(t *testing.T) {
	ctx := context.Background()
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "managed-delete-api")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "managed-delete-api.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "managed-delete-api", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertHTTPDeletionWorkspace(t, database, project.ID, server.ID, "/srv/managed-delete-api", "managed", 0)
	api := resourceTestAPI(database)

	response := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, map[string]string{"mode": "managed-files"}, api.deleteProject)
	if response.Code != http.StatusAccepted {
		t.Fatalf("managed deletion returned %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Status        string   `json:"status"`
		OperationIDs  []string `json:"operation_ids"`
		RemoteDeleted bool     `json:"remote_deleted"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "deleting" || body.RemoteDeleted || len(body.OperationIDs) != 1 {
		t.Fatalf("unexpected managed deletion response: %#v", body)
	}
	operation, err := database.Operation(ctx, body.OperationIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if operation.Kind != store.ProjectDeleteOperationKind || operation.ProjectID != project.ID || operation.WorkspaceID != workspaceID || operation.WorkspaceWrite != 1 || operation.Status != "queued" {
		t.Fatalf("unexpected deletion operation: %#v", operation)
	}
	var command protocol.GitProjectDeleteCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.ProjectID != project.ID || command.WorkspaceID != workspaceID || command.Path != "/srv/managed-delete-api" {
		t.Fatalf("unexpected deletion command: %#v", command)
	}
	current, err := database.Project(ctx, project.ID)
	if err != nil || current.Status != "deleting" {
		t.Fatalf("project was not marked deleting: %#v %v", current, err)
	}
	var auditCount int
	if err := database.DB.GetContext(ctx, &auditCount, database.Q("SELECT COUNT(*) FROM audit_log WHERE action='project.delete.managed_files' AND resource_id=?"), project.ID); err != nil || auditCount != 1 {
		t.Fatalf("managed deletion was not audited: %d %v", auditCount, err)
	}
}

func TestProjectDeletionRejectsActiveDependenciesAndInvalidMode(t *testing.T) {
	ctx := context.Background()
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "blocked-delete-api")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "blocked-delete-api.local"}); err != nil {
		t.Fatal(err)
	}
	project, err := database.CreateProject(ctx, "blocked-delete-api", "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := insertHTTPDeletionWorkspace(t, database, project.ID, server.ID, "/srv/blocked-delete-api", "managed", 0)
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO codex_threads(id,workspace_id,title,status) VALUES(?,?,?,'running')"), store.NewID(), workspaceID, "running"); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)

	for _, mode := range []string{"metadata-only", "managed-files"} {
		response := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, map[string]string{"mode": mode}, api.deleteProject)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "active-codex-tasks") {
			t.Fatalf("%s active task deletion returned %d: %s", mode, response.Code, response.Body.String())
		}
	}
	invalid := projectResourceRequest(t, http.MethodDelete, "/api/projects/"+project.ID, project.ID, map[string]string{"mode": "remote-too"}, api.deleteProject)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode returned %d: %s", invalid.Code, invalid.Body.String())
	}
}

func insertHTTPDeletionWorkspace(t *testing.T, database *store.Store, projectID, serverID, path, mode string, dirty int) string {
	t.Helper()
	id := store.NewID()
	if _, err := database.DB.ExecContext(context.Background(), database.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,branch,commit_sha,dirty) VALUES(?,?,?,?,?,?,'ready','primary','main','abc',?)`), id, projectID, serverID, path, path, mode, dirty); err != nil {
		t.Fatal(err)
	}
	return id
}
