package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiGoalRequiresCompatibleAgentAndCanPauseResumeCancel(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "goal-http-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.83", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/goal", Name: "goal"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	input := map[string]any{"prompt": "Verify the tests", "goal": map[string]int{"max_continuations": 4, "max_seconds": 300}}
	start := func() *httptest.ResponseRecorder {
		return threadResourceRequest(t, http.MethodPost, "/turns", thread.ID, input, api.startGeminiTurn)
	}
	if response := start(); response.Code != 409 {
		t.Fatalf("old Agent accepted goal: %d %s", response.Code, response.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.84", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	if response := start(); response.Code != 202 {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	goal, _ := db.GeminiGoal(ctx, thread.ID)
	if goal == nil || goal.Status != "active" || goal.Objective != "Verify the tests" {
		t.Fatalf("goal not saved: %+v", goal)
	}
	control := func(id, action string, body any) *httptest.ResponseRecorder {
		route := chi.NewRouteContext()
		route.URLParams.Add("threadID", thread.ID)
		route.URLParams.Add("goalID", id)
		route.URLParams.Add("action", action)
		requestContext := context.WithValue(ctx, chi.RouteCtxKey, route)
		requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
		return directJSONRequest(t, http.MethodPost, "/goals", body, nil, func(w http.ResponseWriter, r *http.Request) { api.controlGeminiGoal(w, r.WithContext(requestContext)) })
	}
	if response := control("stale", "pause", map[string]any{}); response.Code != 409 {
		t.Fatalf("stale goal controlled: %d", response.Code)
	}
	if response := control(goal.ID, "pause", map[string]any{}); response.Code != 202 {
		t.Fatalf("pause: %d %s", response.Code, response.Body.String())
	}
	goal, _ = db.GeminiGoal(ctx, thread.ID)
	if goal.Status != "paused" {
		t.Fatalf("pause not saved: %+v", goal)
	}
	if err := db.UpdateGeminiThread(ctx, thread.ID, thread.Title, true); err != nil {
		t.Fatal(err)
	}
	if response := threadResourceRequest(t, http.MethodGet, "/goal", thread.ID, nil, api.geminiGoal); response.Code != 200 {
		t.Fatalf("archived goal was not readable: %d %s", response.Code, response.Body.String())
	}
	if response := control(goal.ID, "cancel", map[string]any{}); response.Code != 409 {
		t.Fatalf("archived goal was modified: %d", response.Code)
	}
	if err := db.UpdateGeminiThread(ctx, thread.ID, thread.Title, false); err != nil {
		t.Fatal(err)
	}
	if response := control(goal.ID, "resume", map[string]int{"max_continuations": 0, "max_seconds": 300}); response.Code != 400 {
		t.Fatalf("invalid limits accepted: %d", response.Code)
	}
	if response := control(goal.ID, "resume", map[string]int{"max_continuations": 5, "max_seconds": 600}); response.Code != 202 {
		t.Fatalf("resume: %d %s", response.Code, response.Body.String())
	}
	if response := control(goal.ID, "cancel", map[string]any{}); response.Code != 202 {
		t.Fatalf("cancel: %d %s", response.Code, response.Body.String())
	}
	goal, _ = db.GeminiGoal(ctx, thread.ID)
	if goal.Status != "cancelled" {
		t.Fatalf("cancel not saved: %+v", goal)
	}
}
