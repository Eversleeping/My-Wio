package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestUserInputQueuesValidatedAnswersAndResolvesRequest(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "user-input-token")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/input", Name: "input"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := db.CreateThread(ctx, workspaces[0].ID, "input")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	detail := `{"questions":[{"id":"q","question":"Pick","options":[{"label":"A"},{"label":"B"}]}]}`
	if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO approvals(id,thread_id,request_id,kind,detail,status,expires_at) VALUES(?,?,?,?,?,'pending',?)"), "input-request", thread.ID, "7", protocol.UserInputRequestMethod, detail, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("approvalID", "input-request")
	requestContext := context.WithValue(ctx, chi.RouteCtxKey, route)
	requestContext = context.WithValue(requestContext, sessionContextKey{}, store.Session{UserID: "test-user"})
	handler := func(w http.ResponseWriter, r *http.Request) { api.decideApproval(w, r.WithContext(requestContext)) }
	for _, body := range []any{
		map[string]any{"decision": "approved"},
		map[string]any{"decision": "answered", "answers": map[string]any{"q": map[string]any{"answers": []string{"invented"}}}},
		map[string]any{"decision": "answered", "answers": map[string]any{}},
	} {
		response := directJSONRequest(t, http.MethodPost, "/api/approvals/input-request/decision", body, nil, handler)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid answer: %d %s", response.Code, response.Body.String())
		}
	}
	body := map[string]any{"decision": "answered", "answers": map[string]any{"q": map[string]any{"answers": []string{"B"}}}}
	response := directJSONRequest(t, http.MethodPost, "/api/approvals/input-request/decision", body, nil, handler)
	if response.Code != http.StatusAccepted {
		t.Fatalf("answer: %d %s", response.Code, response.Body.String())
	}
	operations, err := db.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations: %+v %v", operations, err)
	}
	var command protocol.ApprovalDecisionCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &command); err != nil {
		t.Fatal(err)
	}
	if command.ThreadID != thread.ID || command.RequestID != "7" || command.Answers["q"].Answers[0] != "B" {
		t.Fatalf("wrong routing: %+v", command)
	}
	response = directJSONRequest(t, http.MethodPost, "/api/approvals/input-request/decision", body, nil, handler)
	if response.Code != http.StatusNotFound {
		t.Fatalf("duplicate accepted: %d", response.Code)
	}
}
