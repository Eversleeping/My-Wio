package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeSkillsDiscoveryGatesAndWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "claude-skills-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/skills", Name: "skills"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	workspace := workspaces[0]
	a := resourceTestAPI(db)
	queue := func() *httptest.ResponseRecorder {
		return workspaceResourceRequest(t, "POST", "/", workspace.ID, nil, a.refreshWorkspaceClaudeSkills)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.93", ClaudeReady: true, ClaudeVersion: "2.1.318"}); err != nil {
		t.Fatal(err)
	}
	if w := queue(); w.Code != 409 || !strings.Contains(w.Body.String(), "0.2.94") {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.94"}); err != nil {
		t.Fatal(err)
	}
	if w := queue(); w.Code != 409 || !strings.Contains(w.Body.String(), "ready") {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.94", ClaudeReady: true, ClaudeVersion: "2.1.318"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE servers SET last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-time.Hour), server.ID); err != nil {
		t.Fatal(err)
	}
	if w := queue(); w.Code != 409 {
		t.Fatal("offline", w.Code)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.94", ClaudeReady: true, ClaudeVersion: "2.1.318"}); err != nil {
		t.Fatal(err)
	}
	w := queue()
	var queued struct {
		OperationID string `json:"operation_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &queued) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	op, err := db.Operation(ctx, queued.OperationID)
	if err != nil || op.Kind != "claude.skills.list" || op.WorkspaceID != workspace.ID || op.WorkspaceWrite != 0 {
		t.Fatalf("wrong discovery operation: %+v %v", op, err)
	}
	get := func(workspaceID string) *httptest.ResponseRecorder {
		route := chi.NewRouteContext()
		route.URLParams.Add("workspaceID", workspaceID)
		route.URLParams.Add("operationID", op.ID)
		r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		a.workspaceClaudeSkillsResult(w, r)
		return w
	}
	if w := get("another-workspace"); w.Code != 404 {
		t.Fatal("cross-workspace discovery exposed", w.Code)
	}
	if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: op.ID, Status: "succeeded", Data: json.RawMessage(`{"skills":[{"name":"plugin:review","description":"Review changes","argument_hint":"[scope]"}]}`)}); err != nil {
		t.Fatal(err)
	}
	if w := get(workspace.ID); w.Code != 200 || !strings.Contains(w.Body.String(), "plugin:review") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE agent_operations SET result_data=? WHERE id=?"), `{"skills":[{"name":"invalid name"}]}`, op.ID); err != nil {
		t.Fatal(err)
	}
	if w := get(workspace.ID); w.Code != 500 {
		t.Fatal("invalid skill name accepted", w.Code)
	}
	thread, err := db.CreateClaudeThread(ctx, workspace.ID, "sonnet", "default", "")
	if err != nil || !thread.SkillsSupported {
		t.Fatal("skill capability missing", err)
	}
}
