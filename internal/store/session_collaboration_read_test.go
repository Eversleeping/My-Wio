package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
	"modernc.org/sqlite"
)

func TestCollaborationResultDoesNotLoadUnrelatedHistory(t *testing.T) {
	var unrelatedReads atomic.Int32
	function := "collaboration_payload_" + strings.ReplaceAll(NewID(), "-", "")
	if err := sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if payload, ok := args[0].(string); ok && strings.Contains(payload, "unrelated-history-marker") {
			unrelatedReads.Add(1)
		}
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	f := newCollaborationFixture(t, testStore(t), "codex")
	ctx := context.Background()
	if _, err := f.db.AddEvent(ctx, protocol.StreamEvent{StreamID: f.source.ID, Kind: "codex.item.completed", Payload: json.RawMessage(`{"text":"unrelated-history-marker"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.CompleteOperation(ctx, protocol.OperationResult{OperationID: f.operation, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetThreadStatus(ctx, f.source.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	thread, err := f.db.Thread(ctx, f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.operation, _, err = f.db.QueueCodexTurn(ctx, thread, protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "current turn"})
	if err != nil {
		t.Fatal(err)
	}
	// The view counts actual payload reads, including reads later discarded by
	// the API. No database writes are needed after installing it.
	if _, err = f.db.DB.ExecContext(ctx, "ALTER TABLE events RENAME TO measured_events"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.DB.ExecContext(ctx, "CREATE VIEW events AS SELECT event_id,stream_id,sequence,kind,occurred_at,"+function+"(payload) AS payload FROM measured_events"); err != nil {
		t.Fatal(err)
	}
	reply := f.call(t, "wio_get_result", sessiontools.Params{Session: f.source, OperationID: f.operation})
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	rows := reply.Data.(map[string]any)["messages"].([]protocol.StreamEvent)
	if len(rows) != 1 || !strings.Contains(string(rows[0].Payload), "current turn") {
		t.Fatalf("incorrect turn messages: %+v", rows)
	}
	if unrelatedReads.Load() != 0 {
		t.Fatalf("result loaded unrelated history %d times", unrelatedReads.Load())
	}
}
