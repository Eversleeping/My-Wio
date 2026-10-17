package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestWorkspaceFileWritesThroughAuthenticatedRouter(t *testing.T) {
	ctx := context.Background()
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "file-write-token")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.56"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/files", Name: "files"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	workspaceID := workspaces[0].ID
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspaceID); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "file-user", Username: "file-user", AuthMode: store.AuthModePassword}
	if err := database.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(ctx, user.ID, store.HashToken("file-session"), "csrf", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	handler := New(database, api.hub, api.gateway, api.vault, api.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	request := func(method, path string, body any, csrf bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "file-session"})
		r.Header.Set("Content-Type", "application/json")
		if csrf {
			r.Header.Set("X-CSRF-Token", "csrf")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	path := "/api/workspaces/" + workspaceID + "/files"
	if response := request(http.MethodPost, path, map[string]string{"path": "new.txt", "content": "new"}, false); response.Code != http.StatusForbidden {
		t.Fatalf("CSRF: %d", response.Code)
	}
	for _, unsafe := range []string{"../escape", ".git/config", "nested/../file", "/outside", "C:/outside"} {
		if response := request(http.MethodPost, path, map[string]string{"path": unsafe, "content": "new"}, true); response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe path %s: %d", unsafe, response.Code)
		}
	}
	response := request(http.MethodPost, path, map[string]string{"path": "docs/new.txt", "content": "中文\r\n"}, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var queued struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	op, err := database.Operation(ctx, queued.ID)
	if err != nil || op.Kind != "workspace.file.write" || op.WorkspaceWrite != 1 {
		t.Fatalf("operation: %#v %v", op, err)
	}
	var command protocol.WorkspaceFileWriteCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.Root != "/srv/files" || command.Path != "docs/new.txt" || !command.Create || string(command.Content) != "中文\r\n" {
		t.Fatalf("command: %#v", command)
	}
	progress := request(http.MethodGet, "/api/operations/"+queued.ID, nil, false)
	if progress.Code != 200 || strings.Contains(progress.Body.String(), "content") || strings.Contains(progress.Body.String(), "payload") {
		t.Fatalf("unsafe operation status: %d %s", progress.Code, progress.Body.String())
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodPut, "/api/workspaces/"+workspaceID+"/file-content", map[string]string{"path": "docs/new.txt", "content": "edited", "expected_hash": workspaceContentHash("中文\r\n")}, true)
	if response.Code != 202 {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	op, _ = database.Operation(ctx, queued.ID)
	_ = json.Unmarshal([]byte(op.Payload), &command)
	if command.Create || command.ExpectedHash != workspaceContentHash("中文\r\n") {
		t.Fatalf("incorrect edit command: %#v", command)
	}
	if response := request(http.MethodPost, path, map[string]string{"path": "busy", "content": ""}, true); response.Code != 409 {
		t.Fatalf("concurrent write: %d", response.Code)
	}
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='observed' WHERE id=?"), workspaceID); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodPost, path, map[string]string{"path": "readonly", "content": ""}, true); response.Code != 409 {
		t.Fatalf("observed write: %d", response.Code)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET management_mode='managed' WHERE id=?"), workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.55"}); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodPost, path, map[string]string{"path": "old-agent", "content": ""}, true); response.Code != 409 {
		t.Fatalf("old agent write: %d", response.Code)
	}
}
