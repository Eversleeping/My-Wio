package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestGeminiThinkingPersistsDispatchesAndCanResetToDefault(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "thinking-agent")
	db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.68"})
	db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/thinking", Name: "thinking"}}})
	workspaces, _ := db.ListWorkspaces(ctx)
	api := resourceTestAPI(db)
	input := map[string]string{"workspace_id": workspaces[0].ID, "model": "gemini-3-flash-preview", "thinking_level": "low"}
	create := func() int {
		return directJSONRequest(t, "POST", "/api/gemini/threads", input, &store.Session{UserID: "test-user"}, api.createGeminiThread).Code
	}
	if create() != 409 {
		t.Fatal("old Agent accepted unsupported thinking control")
	}
	db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.70"})
	w := directJSONRequest(t, "POST", "/api/gemini/threads", input, &store.Session{UserID: "test-user"}, api.createGeminiThread)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var thread store.GeminiThread
	json.Unmarshal(w.Body.Bytes(), &thread)
	if thread.ThinkingLevel != "low" || !thread.ThinkingSupported {
		t.Fatal("thinking configuration not saved")
	}
	path := "/api/gemini/threads/" + thread.ID + "/turns"
	for _, invalid := range []map[string]string{{"prompt": "invalid", "thinking_level": "extreme"}, {"prompt": "conflict", "model": "gemini-3.8-flash-high", "thinking_level": "low"}, {"prompt": "budget model", "model": "gemini-2.5-pro", "thinking_level": "high"}} {
		if r := threadResourceRequest(t, "POST", path, thread.ID, invalid, api.startGeminiTurn); r.Code != 400 {
			t.Fatal("invalid thinking level accepted", r.Code)
		}
	}
	for _, level := range []string{"medium", ""} {
		r := threadResourceRequest(t, "POST", path, thread.ID, map[string]string{"prompt": "test", "thinking_level": level}, api.startGeminiTurn)
		if r.Code != 202 {
			t.Fatalf("turn: %d %s", r.Code, r.Body.String())
		}
		current, _ := db.GeminiThread(ctx, thread.ID)
		op, _ := db.Operation(ctx, current.OperationID)
		var command protocol.GeminiTurnCommand
		if json.Unmarshal([]byte(op.Payload), &command) != nil || command.ThinkingLevel != level || current.ThinkingLevel != level {
			t.Fatal("thinking setting lost before dispatch")
		}
		if err := db.InterruptGeminiTurn(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
}
