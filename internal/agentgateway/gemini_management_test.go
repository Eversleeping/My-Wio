package agentgateway

import (
	"context"
	"encoding/json"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestGeminiForkResultsUnlockSourceAndScheduledResultsComplete(t *testing.T) {
	db, server := gatewayTestServer(t, "gemini-management")
	ctx := context.Background()
	db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/gemini", Name: "gemini"}}})
	workspaces, _ := db.ListWorkspaces(ctx)
	source, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "test-model", "default")
	if err != nil {
		t.Fatal(err)
	}
	db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET session_id='original' WHERE id=?"), source.ID)
	source, _ = db.GeminiThread(ctx, source.ID)
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, status := range []string{"failed", "malformed", "succeeded"} {
		op, target, err := db.QueueGeminiFork(ctx, source, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(protocol.GeminiForkResult{SessionID: "new-native"})
		resultStatus := status
		if status == "malformed" {
			resultStatus, data = "succeeded", json.RawMessage(`[]`)
		}
		raw, _ := json.Marshal(protocol.OperationResult{OperationID: op, Status: resultStatus, Data: data})
		if err = g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: raw}); err != nil {
			t.Fatal(err)
		}
		current, _ := db.GeminiThread(ctx, source.ID)
		if current.Status != "idle" || current.SessionID != "original" {
			t.Fatal("fork locked or replaced source")
		}
		operation, _ := db.Operation(ctx, op)
		if status != "succeeded" && operation.Status != "failed" {
			t.Fatal("unsuccessful fork was not marked failed")
		}
		_, err = db.GeminiThread(ctx, target)
		if (status == "succeeded") != (err == nil) {
			t.Fatal("failed fork created thread or successful fork missing")
		}
	}
	task, err := db.CreateScheduledTask(ctx, store.ScheduledTaskInput{AgentKind: "gemini", ThreadID: source.ID, Name: "review", Prompt: "reply OK", Schedule: "@hourly", Timezone: "UTC", Enabled: true, ApprovalMode: "default"}, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.QueueGeminiTurn(ctx, source, "reply OK", "test-model", "default"); err != nil {
		t.Fatal(err)
	}
	current, _ := db.GeminiThread(ctx, source.ID)
	db.MarkScheduledTaskRun(ctx, task.ID, "queued", "", current.OperationID, time.Now().UTC())
	raw, _ := json.Marshal(protocol.OperationResult{OperationID: current.OperationID, Status: "succeeded"})
	if err = g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: raw}); err != nil {
		t.Fatal(err)
	}
	updated, _ := db.ScheduledTask(ctx, task.ID)
	if updated.LastRunStatus != "succeeded" {
		t.Fatalf("scheduled result did not propagate: %s", updated.LastRunStatus)
	}
}
