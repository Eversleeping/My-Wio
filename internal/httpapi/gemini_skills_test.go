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
	"github.com/wio-platform/wio/internal/store"
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

func TestGeminiSkillManagementValidationIdempotencyAndBusyServer(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "skill-management-agent")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/skills", Name: "skills"}, {Path: "/srv/other", Name: "other"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	a := resourceTestAPI(db)
	body := map[string]any{"action": "install", "source": "https://github.com/example/skills.git", "scope": "workspace", "request_id": store.NewID()}
	send := func() *httptest.ResponseRecorder {
		return workspaceResourceRequest(t, "POST", "/skills/manage", workspaces[0].ID, body, a.manageWorkspaceGeminiSkill)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.85", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	if r := send(); r.Code != 409 {
		t.Fatalf("old agent: %d", r.Code)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.88", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	body["scope"] = "global"
	if r := send(); r.Code != 400 {
		t.Fatalf("bad scope: %d", r.Code)
	}
	body["scope"] = "workspace"
	body["source"] = "https://user:password@example.com/skills.git"
	if r := send(); r.Code != 400 {
		t.Fatalf("credential source accepted: %d", r.Code)
	}
	body["source"] = "https://github.com/example/skills.git"
	other, err := db.CreateGeminiThread(ctx, workspaces[1].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.QueueGeminiTurn(ctx, other, "Busy", "flash", "default"); err != nil {
		t.Fatal(err)
	}
	if r := send(); r.Code != 409 {
		t.Fatalf("other workspace task ignored: %d", r.Code)
	}
	other, _ = db.GeminiThread(ctx, other.ID)
	if err = db.CompleteGeminiTurn(ctx, other.ID, other.OperationID, "idle"); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteOperation(ctx, protocol.OperationResult{OperationID: other.OperationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	first := send()
	if first.Code != 202 {
		t.Fatalf("manage: %d %s", first.Code, first.Body.String())
	}
	second := send()
	if second.Code != 202 || first.Body.String() != second.Body.String() {
		t.Fatalf("retry duplicated operation: %s %s", first.Body.String(), second.Body.String())
	}
	var started struct {
		ID string `json:"operation_id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &started)
	op, err := db.Operation(ctx, started.ID)
	if err != nil || op.Kind != "gemini.skills.manage" || op.WorkspaceID != workspaces[0].ID || op.WorkspaceWrite != 1 {
		t.Fatalf("operation: %+v %v", op, err)
	}
	body["scope"] = "user"
	if r := send(); r.Code != 409 {
		t.Fatalf("changed retry accepted: %d", r.Code)
	}
	if _, err := db.QueueGeminiTurn(ctx, other, "Must wait", "flash", "default"); err == nil {
		t.Fatal("turn started during skill configuration")
	}
	if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: started.ID, Status: "succeeded", Data: json.RawMessage(`{"skills":[]}`)}); err != nil {
		t.Fatal(err)
	}
	for i, workspace := range workspaces {
		route := chi.NewRouteContext()
		route.URLParams.Add("workspaceID", workspace.ID)
		route.URLParams.Add("operationID", started.ID)
		r := directJSONRequest(t, "GET", "/skills/result", nil, nil, func(w http.ResponseWriter, r *http.Request) {
			a.workspaceGeminiSkillsResult(w, r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
		})
		if (i == 0 && r.Code != 200) || (i != 0 && r.Code != 404) {
			t.Fatalf("result workspace %d: %d %s", i, r.Code, r.Body.String())
		}
	}
}
