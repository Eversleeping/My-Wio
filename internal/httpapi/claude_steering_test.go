package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeSteerAPIValidationAndGating(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "claude-steering")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-steer", Name: "steer"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	request := func(c protocol.ClaudeSteerCommand) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(c)
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
		route := chi.NewRouteContext()
		route.URLParams.Add("threadID", thread.ID)
		r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		a.steerClaudeTurn(w, r)
		return w
	}
	c := protocol.ClaudeSteerCommand{OperationID: uuid.NewString(), RequestID: uuid.NewString(), Prompt: "correct"}
	for _, heartbeat := range []protocol.Heartbeat{{AgentVersion: "0.2.94", ClaudeVersion: "2.1.318 (Claude Code)"}, {AgentVersion: "0.2.95", ClaudeVersion: "2.1.285"}} {
		if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
			t.Fatal(err)
		}
		if w := request(c); w.Code != 409 || !strings.Contains(w.Body.String(), "requires") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.95", ClaudeVersion: "2.1.318 (Claude Code)"}); err != nil {
		t.Fatal(err)
	}
	if w := request(c); w.Code != 409 {
		t.Fatal("idle instruction accepted", w.Code)
	}
	c.RequestID = "invalid"
	if w := request(c); w.Code != 400 {
		t.Fatal("invalid UUID accepted", w.Code)
	}
}
