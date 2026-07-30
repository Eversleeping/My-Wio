package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestScheduledTaskCRUD(t *testing.T) {
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "scheduled-task-token")
	ctx := context.Background()
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "scheduled-node", AgentVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/scheduled", Name: "scheduled"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "scheduled session")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(database)
	session := &store.Session{UserID: "test-user"}
	createdResponse := directJSONRequest(t, http.MethodPost, "/api/scheduled-tasks", map[string]any{
		"thread_id": thread.ID, "name": "Morning check", "prompt": "Review open issues", "schedule": "0 9 * * 1-5", "timezone": "Asia/Shanghai", "enabled": true,
	}, session, api.createScheduledTask)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created store.ScheduledTask
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "Morning check" || created.Schedule != "0 9 * * 1-5" || created.NextRunAt.Before(time.Now().UTC()) {
		t.Fatalf("unexpected scheduled task: %#v", created)
	}
	listResponse := directJSONRequest(t, http.MethodGet, "/api/scheduled-tasks", nil, session, api.scheduledTasks)
	if listResponse.Code != http.StatusOK || !containsTaskJSON(listResponse.Body.Bytes(), created.ID) {
		t.Fatalf("list did not return created task: %d %s", listResponse.Code, listResponse.Body.String())
	}
	updateRoute := chi.NewRouteContext()
	updateRoute.URLParams.Add("taskID", created.ID)
	updateRequestContext := context.WithValue(context.Background(), chi.RouteCtxKey, updateRoute)
	updateRequestContext = context.WithValue(updateRequestContext, sessionContextKey{}, *session)
	updateResponse := directJSONRequest(t, http.MethodPut, "/api/scheduled-tasks/"+created.ID, map[string]any{"prompt": "Review and summarize open issues", "enabled": false}, nil, func(w http.ResponseWriter, r *http.Request) {
		api.updateScheduledTask(w, r.WithContext(updateRequestContext))
	})
	if updateResponse.Code != http.StatusOK || !containsTaskJSON(updateResponse.Body.Bytes(), "Review and summarize") {
		t.Fatalf("update returned %d: %s", updateResponse.Code, updateResponse.Body.String())
	}
	deleteRoute := chi.NewRouteContext()
	deleteRoute.URLParams.Add("taskID", created.ID)
	deleteRequestContext := context.WithValue(context.Background(), chi.RouteCtxKey, deleteRoute)
	deleteRequestContext = context.WithValue(deleteRequestContext, sessionContextKey{}, *session)
	deleteResponse := directJSONRequest(t, http.MethodDelete, "/api/scheduled-tasks/"+created.ID, nil, nil, func(w http.ResponseWriter, r *http.Request) {
		api.deleteScheduledTask(w, r.WithContext(deleteRequestContext))
	})
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete returned %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
}

func containsTaskJSON(raw []byte, value string) bool {
	return strings.Contains(string(raw), value)
}
