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

func TestAsyncQuestionReplyThroughAuthenticatedRouter(t *testing.T) {
	ctx := context.Background()
	database := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, database, "question-reply-token")
	if err := database.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.54"}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/question", Name: "question"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %#v %v", workspaces, err)
	}
	user := store.User{ID: "question-user", Username: "question-user", AuthMode: store.AuthModePassword}
	if err := database.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	const token, csrf = "question-session", "question-csrf"
	if err := database.CreateSession(ctx, user.ID, store.HashToken(token), csrf, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	api := resourceTestAPI(database)
	handler := New(database, api.hub, api.gateway, api.vault, api.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	for _, status := range []string{"running", "idle"} {
		t.Run(status, func(t *testing.T) {
			thread, err := database.CreateThread(ctx, workspaces[0].ID, "Question")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET codex_thread_id=?,status=? WHERE id=?"), "native-"+thread.ID, status, thread.ID); err != nil {
				t.Fatal(err)
			}
			if status == "running" {
				if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.started", Payload: json.RawMessage(`{"turn":{"id":"active-turn"}}`)}); err != nil {
					t.Fatal(err)
				}
			}
			path := "/api/threads/" + thread.ID
			unauthenticated := requestJSON(t, handler, http.MethodGet, path, nil, nil)
			if unauthenticated.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated lookup: %d", unauthenticated.Code)
			}
			response := requestJSON(t, handler, http.MethodGet, path, nil, cookie)
			if response.Code != http.StatusOK {
				t.Fatalf("question status lookup: %d %s", response.Code, response.Body.String())
			}
			var current store.Thread
			if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
				t.Fatal(err)
			}
			if current.ID != thread.ID || current.Status != status || current.WorkspaceID != workspaces[0].ID {
				t.Fatalf("incorrect session: %#v", current)
			}
			endpoint, kind := "/turns", "codex.turn.start"
			if current.Status == "running" {
				endpoint, kind = "/steer", "codex.turn.steer"
			}
			const prompt = "<send_user_message_question_reply>\n[{\"questionItemId\":\"[\\\"request_user_input_async\\\",\\\"call_test\\\",0]\",\"question\":\"去哪里？\",\"answer\":\"古镇\"}]\n</send_user_message_question_reply>"
			body, _ := json.Marshal(map[string]any{"prompt": prompt, "images": []any{}})
			request := httptest.NewRequest(http.MethodPost, path+endpoint, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", csrf)
			request.AddCookie(cookie)
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusAccepted {
				t.Fatalf("question answer: %d %s", response.Code, response.Body.String())
			}
			operations, err := database.PendingOperations(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range operations {
				var command struct {
					ThreadID string `json:"thread_id"`
					Prompt   string `json:"prompt"`
				}
				if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
					t.Fatal(err)
				}
				if command.ThreadID == thread.ID && command.Prompt == prompt && operation.Kind == kind {
					return
				}
			}
			t.Fatalf("answer not queued as %s", kind)
		})
	}
	missing := requestJSON(t, handler, http.MethodGet, "/api/threads/missing", nil, cookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing session: %d %s", missing.Code, missing.Body.String())
	}
}
