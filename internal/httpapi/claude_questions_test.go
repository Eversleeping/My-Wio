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

func TestClaudeQuestionAPIValidationAndGating(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "claude-questions")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/questions", Name: "questions"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	request := func(raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
		route := chi.NewRouteContext()
		route.URLParams.Add("threadID", thread.ID)
		route.URLParams.Add("requestID", "question")
		r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		a.answerClaudeQuestion(w, r)
		return w
	}
	if w := request(`{"answers":{"0":{"answers":["A"]}}}`); w.Code != 409 {
		t.Fatal("ungated old Agent", w.Code)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.98", ClaudeVersion: "2.1.318"}); err != nil {
		t.Fatal(err)
	}
	if w := request(`invalid`); w.Code != 400 {
		t.Fatal("accepted invalid JSON", w.Code)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if _, err := db.QueueClaudeTurn(ctx, thread, "Ask me", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"operation_id": thread.OperationID, "questions": []any{map[string]any{"id": "0", "question": "Which?", "isOther": true}}})
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{EventID: "question", StreamID: thread.ID, Kind: "claude.question", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if w := request(`{"answers":{}}`); w.Code != 409 {
		t.Fatal("accepted missing answers", w.Code)
	}
	if w := request(`{"answers":{"0":{"answers":["A"]}}}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
}
