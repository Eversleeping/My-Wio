package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestStoreErrorStatusClassification(t *testing.T) {
	for name, item := range map[string]struct {
		err    error
		status int
	}{
		"missing":              {sql.ErrNoRows, 404},
		"request collision":    {store.ErrRequestConflict, 409},
		"busy":                 {store.ErrWorkspaceWriteActive, 409},
		"invalid input":        {store.ErrInvalidEditTarget, 400},
		"postgres unique":      {&pgconn.PgError{Code: "23505"}, 409},
		"postgres foreign key": {&pgconn.PgError{Code: "23503"}, 409},
		"postgres check":       {&pgconn.PgError{Code: "23514"}, 400},
		"postgres outage":      {&pgconn.PgError{Code: "08006", Message: "private DB details"}, 500},
		"internal":             {errors.New("private DB details"), 500},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeStoreError(response, item.err, "store failed")
			if response.Code != item.status {
				t.Fatalf("got %d, want %d: %s", response.Code, item.status, response.Body.String())
			}
			if item.status == 500 && response.Body.String() != "{\"error\":\"store failed\"}\n" {
				t.Fatalf("internal error leaked: %s", response.Body.String())
			}
		})
	}
}

func TestRemainingHandlersSeparateStoreFailures(t *testing.T) {
	for name, call := range map[string]func(*API) *httptest.ResponseRecorder{
		"create Codex": func(a *API) *httptest.ResponseRecorder {
			return directJSONRequest(t, http.MethodPost, "/api/threads", map[string]string{"workspace_id": "missing"}, &store.Session{UserID: "test-user"}, a.createThread)
		},
		"create Claude": func(a *API) *httptest.ResponseRecorder {
			return directJSONRequest(t, http.MethodPost, "/api/claude/threads", map[string]string{"workspace_id": "missing"}, &store.Session{UserID: "test-user"}, a.createClaudeThread)
		},
		"create Gemini": func(a *API) *httptest.ResponseRecorder {
			return directJSONRequest(t, http.MethodPost, "/api/gemini/threads", map[string]string{"workspace_id": "missing"}, &store.Session{UserID: "test-user"}, a.createGeminiThread)
		},
		"approval": func(a *API) *httptest.ResponseRecorder {
			return directJSONRequest(t, http.MethodPost, "/api/approvals/missing", map[string]string{"decision": "denied"}, &store.Session{UserID: "test-user"}, a.decideApproval)
		},
		"secret set": func(a *API) *httptest.ResponseRecorder {
			return directJSONRequest(t, http.MethodDelete, "/api/secret-sets/missing", nil, &store.Session{UserID: "test-user"}, a.deleteSecretSet)
		},
	} {
		t.Run(name, func(t *testing.T) {
			db := openBootstrapTestStore(t)
			api := resourceTestAPI(db)
			missingStatus := http.StatusNotFound
			switch name {
			case "create Codex", "create Claude", "create Gemini":
				missingStatus = http.StatusBadRequest
			}
			if response := call(api); response.Code != missingStatus {
				t.Fatalf("missing: %d %s", response.Code, response.Body.String())
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if response := call(api); response.Code != 500 {
				t.Fatalf("closed DB: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCodexHTTPRetriesReplayAfterTurnCompletion(t *testing.T) {
	db := openBootstrapTestStore(t)
	ctx := context.Background()
	server := enrollResourceTestServer(t, db, "codex-request-replay")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.99"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/replay", Name: "replay"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspace: %#v %v", workspaces, err)
	}
	thread, err := db.CreateThread(ctx, workspaces[0].ID, "replay")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	turnInput := map[string]string{"prompt": "hello", "request_id": "turn-key"}
	first := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/turns", thread.ID, turnInput, api.startTurn)
	if first.Code != 202 {
		t.Fatalf("turn: %d %s", first.Code, first.Body.String())
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET status='running',codex_thread_id='native' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "turn.accepted", Payload: json.RawMessage(`{"turn_id":"turn"}`)}); err != nil {
		t.Fatal(err)
	}
	steerInput := map[string]string{"prompt": "correction", "request_id": "steer-key"}
	steering := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/steer", thread.ID, steerInput, api.steerTurn)
	if steering.Code != 202 {
		t.Fatalf("steer: %d %s", steering.Code, steering.Body.String())
	}
	if err := db.SetThreadStatus(ctx, thread.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		body     any
		handler  http.HandlerFunc
		previous *httptest.ResponseRecorder
	}{{turnInput, api.startTurn, first}, {steerInput, api.steerTurn, steering}} {
		retry := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID, thread.ID, item.body, item.handler)
		if retry.Code != 202 || retry.Body.String() != item.previous.Body.String() {
			t.Fatalf("replay: %d %s; first: %s", retry.Code, retry.Body.String(), item.previous.Body.String())
		}
	}
	steerInput["prompt"] = "changed"
	conflict := threadResourceRequest(t, http.MethodPost, "/api/threads/"+thread.ID+"/steer", thread.ID, steerInput, api.steerTurn)
	if conflict.Code != 409 {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	var operations, messages int
	if err := db.DB.GetContext(ctx, &operations, db.Q("SELECT COUNT(*) FROM agent_operations WHERE thread_id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.GetContext(ctx, &messages, db.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND kind='user.message'"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if operations != 2 || messages != 2 {
		t.Fatalf("duplicate execution: operations=%d messages=%d", operations, messages)
	}
}

func TestSQLiteConstraintErrorsHaveClientStatuses(t *testing.T) {
	db := openBootstrapTestStore(t)
	ctx := context.Background()
	for _, item := range []struct {
		query  string
		status int
	}{
		{`INSERT INTO projects(id,name) VALUES('constraint-project','one')`, 0},
		{`INSERT INTO projects(id,name) VALUES('constraint-project','two')`, 409},
		{`INSERT INTO projects(id,name) VALUES('constraint-project-2',NULL)`, 400},
		{`INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at) VALUES('constraint-session','missing','unique-token','csrf',CURRENT_TIMESTAMP)`, 409},
	} {
		_, err := db.DB.ExecContext(ctx, item.query)
		if item.status == 0 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatal("constraint did not fail")
		}
		response := httptest.NewRecorder()
		writeStoreError(response, err, "store failed")
		if response.Code != item.status {
			t.Fatalf("%s: %d %s (%v)", item.query, response.Code, response.Body.String(), err)
		}
	}
}
