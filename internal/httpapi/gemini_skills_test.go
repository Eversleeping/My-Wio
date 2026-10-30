package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiSkillDiscoveryChecksVersionsAndWorkspace(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "skills-agent")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/skills", Name: "skills"}, {Path: "/srv/other", Name: "other"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("workspaces: %+v %v", workspaces, err)
	}
	a := resourceTestAPI(db)
	start := func() string {
		w := workspaceResourceRequest(t, http.MethodPost, "/skills", workspaces[0].ID, nil, a.refreshWorkspaceGeminiSkills)
		var response struct {
			ID string `json:"operation_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if w.Code != http.StatusAccepted || response.ID == "" {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		return response.ID
	}
	for _, version := range []protocol.Heartbeat{{AgentVersion: "0.2.82", GeminiVersion: "0.66.0"}, {AgentVersion: "0.2.83", GeminiVersion: "0.65.0"}} {
		if err := db.Heartbeat(ctx, server.ID, version); err != nil {
			t.Fatal(err)
		}
		w := workspaceResourceRequest(t, http.MethodPost, "/skills", workspaces[0].ID, nil, a.refreshWorkspaceGeminiSkills)
		if w.Code != http.StatusConflict {
			t.Fatalf("outdated versions accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.83", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	id := start()
	op, err := db.Operation(ctx, id)
	if err != nil || op.Kind != "gemini.skills.list" || op.WorkspaceID != workspaces[0].ID {
		t.Fatalf("operation scope: %+v %v", op, err)
	}
	var command protocol.GeminiSkillsCommand
	if json.Unmarshal([]byte(op.Payload), &command) != nil || command.Workspace != workspaces[0].Path {
		t.Fatalf("wrong workspace payload: %s", op.Payload)
	}
	data := json.RawMessage(`{"skills":[{"name":"review","description":"Review code","enabled":true,"builtin":false,"api_key":"must-not-leak"}]}`)
	if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: id, Status: "succeeded", Data: data}); err != nil {
		t.Fatal(err)
	}
	result := func(workspaceID string) *httptest.ResponseRecorder {
		route := chi.NewRouteContext()
		route.URLParams.Add("workspaceID", workspaceID)
		route.URLParams.Add("operationID", id)
		return directJSONRequest(t, http.MethodGet, "/skills/result", nil, nil, func(w http.ResponseWriter, r *http.Request) {
			a.workspaceGeminiSkillsResult(w, r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
		})
	}
	if w := result(workspaces[1].ID); w.Code != http.StatusNotFound {
		t.Fatalf("other workspace read operation: %d", w.Code)
	}
	if w := result(workspaces[0].ID); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"review"`) || strings.Contains(w.Body.String(), "must-not-leak") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("discovery response: %d %s", w.Code, w.Body.String())
	}
}
