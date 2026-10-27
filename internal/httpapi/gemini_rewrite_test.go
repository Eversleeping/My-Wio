package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiRewriteValidatesAgentAndTargetWithoutRemovingHistory(t *testing.T) {
	ctx := context.Background()
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "gemini-rewrite-api")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.65"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/rewrite-api", Name: "rewrite-api"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "model", "default")
	if err != nil {
		t.Fatal(err)
	}
	api := resourceTestAPI(db)
	body := map[string]string{"prompt": "replacement"}
	if response := rewriteResourceRequest(t, thread.ID, "missing", body, api.startGeminiTurn); response.Code != 409 || !strings.Contains(response.Body.String(), "0.2.66") {
		t.Fatalf("old Agent accepted rewrite: %d %s", response.Code, response.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.66"}); err != nil {
		t.Fatal(err)
	}
	if response := rewriteResourceRequest(t, thread.ID, "missing", body, api.startGeminiTurn); response.Code != 409 {
		t.Fatalf("invalid target accepted: %d", response.Code)
	}
	event, err := db.QueueGeminiTurn(ctx, thread, "original", "model", "default")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := db.GeminiThread(ctx, thread.ID)
	payload, _ := json.Marshal(map[string]string{"operation_id": current.OperationID, "session_id": "native", "timestamp": "2026-10-25T00:00:00Z"})
	if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "gemini.init", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteOperation(ctx, protocol.OperationResult{OperationID: current.OperationID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteGeminiTurn(ctx, current.ID, current.OperationID, "idle"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET session_id='native' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if response := rewriteResourceRequest(t, thread.ID, event.EventID, map[string]string{"prompt": "replacement", "approval_mode": "invalid"}, api.startGeminiTurn); response.Code != 400 {
		t.Fatal("invalid mode accepted")
	}
	body["approval_mode"] = "yolo"
	if response := rewriteResourceRequest(t, thread.ID, event.EventID, body, api.startGeminiTurn); response.Code != 409 || !strings.Contains(response.Body.String(), buildinfo.MinimumGeminiYoloVersion) {
		t.Fatalf("old Agent accepted yolo rewrite: %d %s", response.Code, response.Body.String())
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: buildinfo.MinimumGeminiYoloVersion}); err != nil {
		t.Fatal(err)
	}
	if response := rewriteResourceRequest(t, thread.ID, event.EventID, body, api.startGeminiTurn); response.Code != 202 {
		t.Fatalf("rewrite: %d %s", response.Code, response.Body.String())
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if !current.RewriteSupported || current.Status != "queued" {
		t.Fatalf("missing capability/claim: %+v", current)
	}
	op, err := db.Operation(ctx, current.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var command protocol.GeminiTurnCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil || command.ApprovalMode != "yolo" || command.Rewrite == nil {
		t.Fatalf("rewrite lost approval mode: %+v %v", command, err)
	}
	events, _ := db.Events(ctx, thread.ID, 0, 500)
	if len(events) < 2 || events[0].EventID != event.EventID {
		t.Fatal("queued rewrite changed original history")
	}
	if response := rewriteResourceRequest(t, thread.ID, event.EventID, body, api.startGeminiTurn); response.Code != 409 {
		t.Fatal("concurrent rewrite accepted")
	}
	if err := db.InterruptGeminiTurn(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateGeminiThread(ctx, thread.ID, "archived", true); err != nil {
		t.Fatal(err)
	}
	if response := rewriteResourceRequest(t, thread.ID, event.EventID, body, api.startGeminiTurn); response.Code != 409 {
		t.Fatal("archived rewrite accepted")
	}
}
