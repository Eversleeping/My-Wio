//go:build postgresintegration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Holding only the advisory lock must block every event-writing path. On the
// old code these writers commit successfully while the lock is still held.
func assertRemainingEventWritersUseStreamLock(t *testing.T, db *Store) {
	t.Helper()
	for _, kind := range []string{"claude turn", "claude steer", "claude rewrite", "gemini turn", "gemini steer", "gemini confirm", "gemini rewrite"} {
		t.Run(kind, func(t *testing.T) {
			fixture := batchBFixture(t, db)
			setupCtx := context.Background()
			if err := db.Heartbeat(setupCtx, fixture.ServerID, protocol.Heartbeat{AgentVersion: "0.2.99", ClaudeVersion: "2.1.318 (Claude Code)", ClaudeReady: true, GeminiVersion: "0.66.0"}); err != nil {
				t.Fatal(err)
			}
			var stream string
			var write func(context.Context) error
			if kind[:6] == "claude" {
				thread, err := db.CreateClaudeThread(setupCtx, fixture.WorkspaceID, "sonnet", "default", "")
				if err != nil {
					t.Fatal(err)
				}
				stream = thread.ID
				switch kind {
				case "claude turn":
					write = func(ctx context.Context) error {
						_, err := db.QueueClaudeTurn(ctx, thread, "start", "sonnet", "default", "")
						return err
					}
				case "claude steer":
					if _, err := db.QueueClaudeTurn(setupCtx, thread, "start", "sonnet", "default", ""); err != nil {
						t.Fatal(err)
					}
					thread, err = db.ClaudeThread(setupCtx, thread.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.DB.ExecContext(setupCtx, db.Q("UPDATE agent_operations SET status='running' WHERE id=?"), thread.OperationID); err != nil {
						t.Fatal(err)
					}
					if _, err := db.DB.ExecContext(setupCtx, db.Q("UPDATE claude_threads SET status='running' WHERE id=?"), thread.ID); err != nil {
						t.Fatal(err)
					}
					raw, _ := json.Marshal(map[string]any{"operation_id": thread.OperationID, "ready": true})
					if _, err := db.AddEvent(setupCtx, protocol.StreamEvent{StreamID: stream, Kind: "claude.steer.ready", Payload: raw}); err != nil {
						t.Fatal(err)
					}
					command := protocol.ClaudeSteerCommand{ThreadID: stream, OperationID: thread.OperationID, RequestID: NewID(), Prompt: "steer"}
					write = func(ctx context.Context) error { _, _, err := db.QueueClaudeSteer(ctx, command); return err }
				case "claude rewrite":
					original, err := db.AddEvent(setupCtx, protocol.StreamEvent{StreamID: stream, Kind: "claude.user.message", Payload: json.RawMessage(`{"text":"old"}`)})
					if err != nil {
						t.Fatal(err)
					}
					native := NewID()
					command := protocol.ClaudeTurnCommand{ThreadID: stream, SessionID: native, Prompt: "new", Rewrite: &protocol.ClaudeRewriteTarget{EventID: original.EventID}}
					op, err := db.QueueOperation(setupCtx, fixture.ServerID, "claude.turn.start", command, NewID())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.DB.ExecContext(setupCtx, db.Q("UPDATE claude_threads SET status='running',operation_id=?,session_id=? WHERE id=?"), op, native, stream); err != nil {
						t.Fatal(err)
					}
					write = func(ctx context.Context) error {
						_, _, err := db.CommitClaudeRewrite(ctx, stream, op, NewID())
						return err
					}
				}
			} else {
				thread, err := db.CreateGeminiThread(setupCtx, fixture.WorkspaceID, "flash", "default")
				if err != nil {
					t.Fatal(err)
				}
				stream = thread.ID
				switch kind {
				case "gemini turn":
					write = func(ctx context.Context) error {
						_, err := db.QueueGeminiTurn(ctx, thread, "start", "flash", "default")
						return err
					}
				case "gemini steer", "gemini confirm":
					if _, err := db.QueueGeminiTurn(setupCtx, thread, "start", "flash", "default"); err != nil {
						t.Fatal(err)
					}
					thread, err = db.GeminiThread(setupCtx, stream)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.DB.ExecContext(setupCtx, db.Q("UPDATE agent_operations SET status='running' WHERE id=?"), thread.OperationID); err != nil {
						t.Fatal(err)
					}
					status := "running"
					if kind == "gemini confirm" {
						status = "interrupted"
					}
					if _, err := db.DB.ExecContext(setupCtx, db.Q("UPDATE gemini_threads SET status=? WHERE id=?"), status, stream); err != nil {
						t.Fatal(err)
					}
					if kind == "gemini confirm" {
						write = func(ctx context.Context) error {
							_, err := db.ConfirmGeminiStopped(ctx, stream, thread.OperationID)
							return err
						}
					} else {
						raw, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID})
						if _, err := db.AddEvent(setupCtx, protocol.StreamEvent{StreamID: stream, Kind: "gemini.steer.ready", Payload: raw}); err != nil {
							t.Fatal(err)
						}
						write = func(ctx context.Context) error {
							_, _, err := db.QueueGeminiSteer(ctx, stream, thread.OperationID, NewID(), "steer")
							return err
						}
					}
				case "gemini rewrite":
					original, err := db.AddEvent(setupCtx, protocol.StreamEvent{StreamID: stream, Kind: "gemini.user.message", Payload: json.RawMessage(`{"text":"old"}`)})
					if err != nil {
						t.Fatal(err)
					}
					command := protocol.GeminiTurnCommand{ThreadID: stream, SessionID: "old-native", Prompt: "new", Rewrite: &protocol.GeminiRewriteTarget{EventID: original.EventID}}
					op, err := db.QueueOperation(setupCtx, fixture.ServerID, "gemini.turn.rewrite", command, NewID())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.DB.ExecContext(setupCtx, db.Q("UPDATE gemini_threads SET status='running',operation_id=?,session_id='old-native' WHERE id=?"), op, stream); err != nil {
						t.Fatal(err)
					}
					write = func(ctx context.Context) error {
						_, _, err := db.CommitGeminiRewrite(ctx, stream, op, "new-native")
						return err
					}
				}
			}
			lock, err := db.DB.BeginTxx(setupCtx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			if err := lockEventStream(setupCtx, lock, stream); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(setupCtx, 250*time.Millisecond)
			defer cancel()
			err = write(ctx)
			if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("writer bypassed stream lock or failed early: %v (context: %v)", err, ctx.Err())
			}
		})
	}
}
