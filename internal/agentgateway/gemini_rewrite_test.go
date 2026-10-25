package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
)

func TestGeminiRewriteCommitsOnlyAfterNativeImportAndRejectsStaleOutput(t *testing.T) {
	db, server := gatewayTestServer(t, "gemini-rewrite")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/gemini-rewrite", Name: "rewrite"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "model", "default")
	if err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	emit := func(id, kind string, p map[string]string) {
		t.Helper()
		raw, _ := json.Marshal(protocol.StreamEvent{EventID: id, StreamID: thread.ID, Kind: kind, Payload: mustGeminiJSON(p)})
		if err := g.handleGeminiEvent(ctx, server.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(opID, status string) {
		t.Helper()
		raw, _ := json.Marshal(protocol.OperationResult{OperationID: opID, Status: status, Message: "done"})
		if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: raw}); err != nil {
			t.Fatal(err)
		}
	}
	var target protocol.StreamEvent
	oldOperation := ""
	for i, prompt := range []string{"keep", "edit-original", "discard-later"} {
		current, _ := db.GeminiThread(ctx, thread.ID)
		user, err := db.QueueGeminiTurn(ctx, current, prompt, "model", "default")
		if err != nil {
			t.Fatal(err)
		}
		current, _ = db.GeminiThread(ctx, thread.ID)
		timestamp := []string{"2026-10-25T00:00:00Z", "2026-10-25T00:01:00Z", "2026-10-25T00:02:00Z"}[i]
		emit(current.OperationID+":init", "gemini.init", map[string]string{"operation_id": current.OperationID, "session_id": "native-original", "timestamp": timestamp})
		emit(current.OperationID+":answer", "gemini.message", map[string]string{"operation_id": current.OperationID, "role": "assistant", "content": prompt + "-answer"})
		finish(current.OperationID, "succeeded")
		if i == 1 {
			target = user
		}
		oldOperation = current.OperationID
	}
	original, _ := db.Events(ctx, thread.ID, 0, 500)
	last := original[len(original)-1].Sequence
	current, _ := db.GeminiThread(ctx, thread.ID)
	// Invalid/foreign targets roll back the claim and preserve all existing data.
	if _, err := db.QueueGeminiRewrite(ctx, current, "foreign-event", "edit", "model", "default"); err == nil {
		t.Fatal("foreign edit accepted")
	}
	if _, err := db.QueueGeminiRewrite(ctx, current, original[1].EventID, "edit", "model", "default"); err == nil {
		t.Fatal("non-user edit accepted")
	}
	if _, err := db.QueueGeminiRewrite(ctx, current, target.EventID, "revised", "model", "default"); err != nil {
		t.Fatal(err)
	}
	queued, _ := db.GeminiThread(ctx, thread.ID)
	operation, _ := db.Operation(ctx, queued.OperationID)
	var command protocol.GeminiTurnCommand
	_ = json.Unmarshal([]byte(operation.Payload), &command)
	if operation.Kind != "gemini.turn.rewrite" || operation.WorkspaceWrite != 1 || command.Rewrite == nil || command.Rewrite.StartedAt != "2026-10-25T00:01:00Z" || command.Rewrite.Before != "2026-10-25T00:02:00Z" {
		t.Fatalf("wrong rewrite operation: %+v %+v", operation, command)
	}
	unchanged, _ := db.Events(ctx, thread.ID, 0, 500)
	if len(unchanged) != len(original) {
		t.Fatal("history changed before native import")
	}
	finish(queued.OperationID, "failed")
	unchanged, _ = db.Events(ctx, thread.ID, 0, 500)
	encoded, _ := json.Marshal(unchanged)
	if !strings.Contains(string(encoded), "discard-later-answer") {
		t.Fatal("failed preparation removed history")
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if current.SessionID != "native-original" {
		t.Fatal("failed rewrite changed native session")
	}
	// A successful retry changes both native session and transcript exactly once.
	if _, err := db.QueueGeminiRewrite(ctx, current, target.EventID, "revised", "model", "default"); err != nil {
		t.Fatal(err)
	}
	queued, _ = db.GeminiThread(ctx, thread.ID)
	emit("replacement-init", "gemini.init", map[string]string{"operation_id": queued.OperationID, "session_id": "native-replacement", "timestamp": "2026-10-25T00:03:00Z"})
	rewritten, _ := db.Events(ctx, thread.ID, 0, 500)
	encoded, _ = json.Marshal(rewritten)
	if strings.Contains(string(encoded), "edit-original") || strings.Contains(string(encoded), "discard-later") || !strings.Contains(string(encoded), "keep-answer") || !strings.Contains(string(encoded), "revised") {
		t.Fatalf("incorrect rewritten history: %s", encoded)
	}
	replacementFound := false
	for _, event := range rewritten {
		if event.EventID == queued.OperationID+":user" {
			replacementFound = true
			if event.Sequence <= last || !strings.Contains(string(event.Payload), "history_rewritten") {
				t.Fatal("replacement does not invalidate old cursors")
			}
		}
	}
	if !replacementFound {
		t.Fatal("replacement user event missing")
	}
	emit("replacement-init", "gemini.init", map[string]string{"operation_id": queued.OperationID, "session_id": "native-replacement", "timestamp": "2026-10-25T00:03:00Z"})
	duplicate, _ := db.Events(ctx, thread.ID, 0, 500)
	if len(duplicate) != len(rewritten) {
		t.Fatal("replayed init duplicated rewrite")
	}
	emit("late-old-answer", "gemini.message", map[string]string{"operation_id": oldOperation, "role": "assistant", "content": "STALE"})
	after, _ := db.Events(ctx, thread.ID, 0, 500)
	if len(after) != len(rewritten) {
		t.Fatal("stale output restored discarded history")
	}
	current, _ = db.GeminiThread(ctx, thread.ID)
	if current.SessionID != "native-replacement" || current.Status != "running" {
		t.Fatalf("native binding: %+v", current)
	}
	finish(queued.OperationID, "succeeded")
}

func mustGeminiJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
