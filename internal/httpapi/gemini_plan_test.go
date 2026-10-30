package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiPlanRequiresCompatibleVersionsAndDispatchesACP(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "plan-agent")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/plan", Name: "plan"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	a := resourceTestAPI(db)
	input := map[string]string{"workspace_id": workspaces[0].ID, "model": "flash", "approval_mode": "plan"}
	create := func() *httptest.ResponseRecorder {
		return directJSONRequest(t, http.MethodPost, "/gemini/threads", input, &store.Session{UserID: "test-user"}, a.createGeminiThread)
	}
	for _, heartbeat := range []protocol.Heartbeat{{AgentVersion: "0.2.82", GeminiVersion: "0.66.0"}, {AgentVersion: "0.2.83", GeminiVersion: "0.65.0"}} {
		if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
			t.Fatal(err)
		}
		if w := create(); w.Code != http.StatusConflict {
			t.Fatalf("outdated versions accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.83", GeminiVersion: "0.66.0"}); err != nil {
		t.Fatal(err)
	}
	w := create()
	var thread store.GeminiThread
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &thread) != nil || !thread.PlanSupported {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w = threadResourceRequest(t, http.MethodPost, "/turns", thread.ID, map[string]string{"prompt": "Plan the change", "approval_mode": "plan"}, a.startGeminiTurn)
	if w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	thread, _ = db.GeminiThread(ctx, thread.ID)
	op, err := db.Operation(ctx, thread.OperationID)
	var command protocol.GeminiTurnCommand
	if err != nil || json.Unmarshal([]byte(op.Payload), &command) != nil || !command.Interactive || command.ApprovalMode != "plan" {
		t.Fatalf("plan command: %+v %v", command, err)
	}
}
