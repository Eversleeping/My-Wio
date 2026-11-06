package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestOperationInterruptRouteAuthenticationAndLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "operation-interrupt-agent")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.96"}); err != nil {
		t.Fatal(err)
	}
	user := store.User{ID: "interrupt-user", Username: "interrupt-user", AuthMode: store.AuthModePassword}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user.ID, store.HashToken("interrupt-cookie"), "interrupt-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := resourceTestAPI(db)
	handler := New(db, a.hub, a.gateway, a.vault, a.log, fstest.MapFS{"index.html": {Data: []byte("ok")}}, "http://localhost", true)
	request := func(id string, authenticated, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/operations/"+id+"/interrupt", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "interrupt-cookie"})
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", "interrupt-csrf")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	id, err := db.QueueOperation(ctx, server.ID, "git.workspace.inspect", map[string]string{}, "interrupt-api")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(id, false, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", w.Code, w.Body.String())
	}
	if w := request(id, true, false); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d %s", w.Code, w.Body.String())
	}
	firstID := ""
	for attempt := 0; attempt < 2; attempt++ {
		w := request(id, true, true)
		var result struct {
			OperationID string `json:"operation_id"`
		}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.OperationID == "" {
			t.Fatalf("interruption: %d %s", w.Code, w.Body.String())
		}
		if attempt == 1 && result.OperationID != firstID {
			t.Fatal("duplicate request created a second interruption")
		}
		firstID = result.OperationID
	}
	operation, _ := db.Operation(ctx, id)
	if operation.Status != "queued" {
		t.Fatal("HTTP request claimed actual termination")
	}
	unsafeID, err := db.QueueOperation(ctx, server.ID, "agent.update", map[string]string{}, "interrupt-unsafe")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(unsafeID, true, true); w.Code != http.StatusConflict {
		t.Fatalf("unsafe operation: %d %s", w.Code, w.Body.String())
	}
	if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: id, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if w := request(id, true, true); w.Code != http.StatusConflict {
		t.Fatalf("completed operation: %d %s", w.Code, w.Body.String())
	}
	if w := request("missing", true, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing operation: %d", w.Code)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.95"}); err != nil {
		t.Fatal(err)
	}
	legacyID, err := db.QueueOperation(ctx, server.ID, "inventory.scan", map[string]string{}, "interrupt-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(legacyID, true, true); w.Code != http.StatusConflict {
		t.Fatalf("old Agent accepted unsupported command: %d", w.Code)
	}
}
