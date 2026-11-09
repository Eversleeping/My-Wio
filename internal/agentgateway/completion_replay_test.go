package agentgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func completionReplayFixture(t *testing.T, engine string) (*store.Store, *Gateway, string, string, string) {
	t.Helper()
	db, server := gatewayTestServer(t, "completion-"+engine)
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/completion", Name: "completion"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var threadID, operationID string
	switch engine {
	case "claude":
		thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.QueueClaudeTurn(ctx, thread, "task", "sonnet", "default", ""); err != nil {
			t.Fatal(err)
		}
		thread, err = db.ClaudeThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		threadID, operationID = thread.ID, thread.OperationID
	case "gemini":
		thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.QueueGeminiTurn(ctx, thread, "task", "auto", "default"); err != nil {
			t.Fatal(err)
		}
		thread, err = db.GeminiThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		threadID, operationID = thread.ID, thread.OperationID
	case "codex":
		thread, err := db.CreateThread(ctx, workspaces[0].ID, "completion")
		if err != nil {
			t.Fatal(err)
		}
		operationID, _, err = db.QueueCodexTurn(ctx, thread, protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "task"})
		if err != nil {
			t.Fatal(err)
		}
		threadID = thread.ID
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return db, g, server.ID, threadID, operationID
}

func TestTurnCompletionReplayRepairsDatabaseFailure(t *testing.T) {
	for _, engine := range []string{"claude", "gemini", "codex"} {
		for _, status := range []string{"succeeded", "failed", "cancelled"} {
			if engine == "codex" && status != "failed" {
				continue
			} // Codex success only acknowledges acceptance.
			t.Run(engine+"/"+status, func(t *testing.T) {
				db, g, server, thread, operation := completionReplayFixture(t, engine)
				ctx := context.Background()
				want := map[string]string{"succeeded": "idle", "failed": "failed", "cancelled": "interrupted"}[status]
				trigger := fmt.Sprintf("CREATE TRIGGER fail_completion BEFORE UPDATE OF status ON %s_threads WHEN NEW.status='%s' BEGIN SELECT RAISE(ABORT,'temporary completion failure'); END", engine, want)
				if _, err := db.DB.ExecContext(ctx, trigger); err != nil {
					t.Fatal(err)
				}
				result := protocol.OperationResult{OperationID: operation, Status: status, Message: "stored completion"}
				if err := g.handleOperationResult(ctx, server, result, false); err == nil {
					t.Fatal("fault did not interrupt completion")
				}
				if _, err := db.DB.ExecContext(ctx, "DROP TRIGGER fail_completion"); err != nil {
					t.Fatal(err)
				}
				// A replay may carry stale/conflicting data. Only the persisted result is authoritative.
				if err := g.handleOperationResult(ctx, server, protocol.OperationResult{OperationID: operation, Status: "succeeded", Message: "late conflicting result"}, false); err != nil {
					t.Fatal(err)
				}
				var current string
				if err := db.DB.GetContext(ctx, &current, db.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), thread); err != nil {
					t.Fatal(err)
				}
				if current != want {
					t.Fatalf("completion replay left thread %q, want %q", current, want)
				}
				saved, err := db.Operation(ctx, operation)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Status != status || saved.Result != "stored completion" {
					t.Fatalf("replay replaced persisted result: %+v", saved)
				}
				if err := g.handleOperationResult(ctx, server, result, false); err != nil {
					t.Fatal(err)
				}
				var count int
				eventID := operation + ":completed"
				if engine == "codex" {
					eventID = operation + ":turn-failed"
				}
				if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM events WHERE event_id=?"), eventID); err != nil || count != 1 {
					t.Fatalf("completion event count=%d: %v", count, err)
				}
			})
		}
	}
}

func TestCancelledTurnReplayDoesNotReviveSession(t *testing.T) {
	for _, engine := range []string{"claude", "gemini"} {
		t.Run(engine, func(t *testing.T) {
			db, g, server, threadID, operation := completionReplayFixture(t, engine)
			ctx := context.Background()
			if engine == "claude" {
				thread, err := db.ClaudeThread(ctx, threadID)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.InterruptClaudeTurn(ctx, thread); err != nil {
					t.Fatal(err)
				}
			} else {
				thread, err := db.GeminiThread(ctx, threadID)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.InterruptGeminiTurn(ctx, thread); err != nil {
					t.Fatal(err)
				}
			}
			before, err := db.Events(ctx, threadID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if err = g.handleOperationResult(ctx, server, protocol.OperationResult{OperationID: operation, Status: "succeeded", Data: json.RawMessage(`{"untrusted":"late"}`)}, false); err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.DB.GetContext(ctx, &status, db.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), threadID); err != nil {
				t.Fatal(err)
			}
			saved, err := db.Operation(ctx, operation)
			if err != nil {
				t.Fatal(err)
			}
			after, err := db.Events(ctx, threadID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if status != "interrupted" || saved.Status != "cancelled" || len(before) != len(after) {
				t.Fatalf("late result changed cancelled session: thread=%s operation=%s events=%d/%d", status, saved.Status, len(before), len(after))
			}
		})
	}
}

func TestOldCompletionReplayLeavesNewTurnAlone(t *testing.T) {
	for _, engine := range []string{"claude", "gemini", "codex"} {
		t.Run(engine, func(t *testing.T) {
			db, g, server, threadID, operation := completionReplayFixture(t, engine)
			ctx := context.Background()
			if err := g.handleOperationResult(ctx, server, protocol.OperationResult{OperationID: operation, Status: "failed"}, false); err != nil {
				t.Fatal(err)
			}
			switch engine {
			case "claude":
				thread, err := db.ClaudeThread(ctx, threadID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.QueueClaudeTurn(ctx, thread, "next", "sonnet", "default", ""); err != nil {
					t.Fatal(err)
				}
			case "gemini":
				thread, err := db.GeminiThread(ctx, threadID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.QueueGeminiTurn(ctx, thread, "next", "auto", "default"); err != nil {
					t.Fatal(err)
				}
			case "codex":
				thread, err := db.Thread(ctx, threadID)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = db.QueueCodexTurn(ctx, thread, protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "next"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.handleOperationResult(ctx, server, protocol.OperationResult{OperationID: operation, Status: "failed"}, false); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := db.DB.GetContext(ctx, &status, db.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), threadID); err != nil {
				t.Fatal(err)
			}
			if status != "queued" {
				t.Fatalf("old result changed the new turn: %s", status)
			}
		})
	}
}

func TestInterruptCompletionReplayRepairsDatabaseFailure(t *testing.T) {
	for _, engine := range []string{"claude", "gemini"} {
		t.Run(engine, func(t *testing.T) {
			db, g, server, thread, operation := completionReplayFixture(t, engine)
			ctx := context.Background()
			if _, err := db.ClaimGeminiDelivery(ctx, operation); err != nil {
				t.Fatal(err)
			}
			var interrupt string
			if engine == "claude" {
				current, err := db.ClaudeThread(ctx, thread)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.InterruptClaudeTurn(ctx, current); err != nil {
					t.Fatal(err)
				}
			} else {
				current, err := db.GeminiThread(ctx, thread)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.InterruptGeminiTurn(ctx, current); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.DB.GetContext(ctx, &interrupt, db.Q("SELECT id FROM agent_operations WHERE kind=?"), engine+".turn.interrupt"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.DB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER fail_completion BEFORE UPDATE OF status ON %s_threads WHEN NEW.status='interrupted' BEGIN SELECT RAISE(ABORT,'temporary completion failure'); END", engine)); err != nil {
				t.Fatal(err)
			}
			receipt, _ := json.Marshal(protocol.GeminiInterruptResult{OperationID: operation, Stopped: true})
			result := protocol.OperationResult{OperationID: interrupt, Status: "succeeded", Data: receipt}
			if err := g.handleOperationResult(ctx, server, result, false); err == nil {
				t.Fatal("fault did not interrupt completion")
			}
			if _, err := db.DB.ExecContext(ctx, "DROP TRIGGER fail_completion"); err != nil {
				t.Fatal(err)
			}
			if err := g.handleOperationResult(ctx, server, result, false); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := db.DB.GetContext(ctx, &status, db.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), thread); err != nil {
				t.Fatal(err)
			}
			if status != "interrupted" {
				t.Fatalf("interrupt replay left thread %s", status)
			}
		})
	}
}
