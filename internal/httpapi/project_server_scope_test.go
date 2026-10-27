package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestProjectSidebarActionsRequireAndRespectServerScope(t *testing.T) {
	database := openBootstrapTestStore(t)
	ctx := context.Background()
	a := enrollResourceTestServer(t, database, "scoped-a")
	b := enrollResourceTestServer(t, database, "scoped-b")
	for _, server := range []store.Server{a, b} {
		if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/same", Name: "same", RemoteURL: "https://example.com/scope.git"}}}); err != nil {
			t.Fatal(err)
		}
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 2 {
		t.Fatal(err)
	}
	projectID := workspaces[0].ProjectID
	api := resourceTestAPI(database)
	patch := func(body map[string]any) int {
		response := projectResourceRequest(t, http.MethodPatch, "/api/projects/"+projectID, projectID, body, api.updateProject)
		return response.Code
	}
	if code := patch(map[string]any{"server_id": a.ID, "pinned": true}); code != http.StatusOK {
		t.Fatalf("pin returned %d", code)
	}
	pa, err := database.ProjectOnServer(ctx, projectID, a.ID)
	pb, berr := database.ProjectOnServer(ctx, projectID, b.ID)
	if err != nil || berr != nil || pa.PinnedAt == nil || pb.PinnedAt != nil {
		t.Fatal("API pin crossed server boundary")
	}
	for _, body := range []map[string]any{{"server_id": "", "pinned": true}, {"server_id": a.ID, "description": "invalid scope"}, {"server_id": a.ID, "archived": true}} {
		if code := patch(body); code != http.StatusBadRequest {
			t.Fatalf("invalid scoped patch accepted: %d", code)
		}
	}
	if code := patch(map[string]any{"server_id": "missing", "pinned": true}); code != http.StatusNotFound {
		t.Fatalf("unknown server returned %d", code)
	}
	var auditDetail string
	if err := database.DB.GetContext(ctx, &auditDetail, database.Q("SELECT detail FROM audit_log WHERE action='project.update' AND resource_id=? ORDER BY occurred_at DESC LIMIT 1"), projectID); err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(auditDetail), &detail); err != nil || detail["server_id"] != a.ID {
		t.Fatal("audit omitted server scope")
	}
	for _, ws := range workspaces {
		if _, err := database.CreateThread(ctx, ws.ID, "Codex"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.CreateGeminiThread(ctx, ws.ID, "flash", "default"); err != nil {
			t.Fatal(err)
		}
	}
	for _, server := range []store.Server{a, b} {
		if code := patch(map[string]any{"server_id": server.ID, "hidden": true}); code != http.StatusOK {
			t.Fatalf("hide returned %d", code)
		}
	}
	response := directJSONRequest(t, http.MethodGet, "/api/projects/hidden", nil, nil, api.hiddenServerProjects)
	var hidden []store.HiddenServerProject
	if err := json.Unmarshal(response.Body.Bytes(), &hidden); err != nil || response.Code != http.StatusOK || len(hidden) != 2 {
		t.Fatalf("hidden project list: %d %s %v", response.Code, response.Body.String(), err)
	}
	for _, item := range hidden {
		if item.ProjectID != projectID || item.ProjectName != "same" || item.ServerName == "" || (item.ServerID != a.ID && item.ServerID != b.ID) {
			t.Fatalf("invalid hidden project: %#v", item)
		}
	}
	if code := patch(map[string]any{"server_id": a.ID, "hidden": false}); code != http.StatusOK {
		t.Fatalf("restore returned %d", code)
	}
	response = directJSONRequest(t, http.MethodGet, "/api/projects/hidden", nil, nil, api.hiddenServerProjects)
	if err := json.Unmarshal(response.Body.Bytes(), &hidden); err != nil || len(hidden) != 1 || hidden[0].ServerID != b.ID {
		t.Fatalf("restore crossed server boundary: %s %v", response.Body.String(), err)
	}
	for _, handler := range []http.HandlerFunc{api.archiveProjectThreads, api.archiveGeminiProject} {
		for _, query := range []string{"?server_id=", "?server_id=" + a.ID + "&server_id=" + b.ID, "?server_id=missing"} {
			response := projectResourceRequest(t, http.MethodPost, "/api/projects/"+projectID+"/threads/archive"+query, projectID, map[string]any{}, handler)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
				t.Fatalf("invalid archive scope accepted: %d", response.Code)
			}
		}
		response := projectResourceRequest(t, http.MethodPost, "/api/projects/"+projectID+"/threads/archive?server_id="+a.ID, projectID, map[string]any{}, handler)
		if response.Code != http.StatusOK {
			t.Fatalf("scoped archive: %d %s", response.Code, response.Body.String())
		}
	}
	threads, err := database.ListThreads(ctx)
	gs, gerr := database.GeminiThreads(ctx, false)
	if err != nil || gerr != nil || len(threads) != 1 || len(gs) != 1 || threads[0].ServerID != b.ID || gs[0].ServerID != b.ID {
		t.Fatal("archive API changed other server")
	}
}
