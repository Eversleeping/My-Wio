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

func TestFlushRunsClaudeAndGeminiSessionsIndependently(t *testing.T) {
	for _, engines := range [][2]string{{"claude", "claude"}, {"gemini", "gemini"}, {"claude", "gemini"}} {
		t.Run(strings.Join(engines[:], "-"), func(t *testing.T) {
			ctx := context.Background()
			db, server := gatewayTestServer(t, "parallel-sessions")
			if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/parallel-sessions", Name: "parallel"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, err := db.ListWorkspaces(ctx)
			if err != nil || len(workspaces) != 1 {
				t.Fatalf("workspaces: %+v %v", workspaces, err)
			}
			gateway := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			stream := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 2)}
			ids := [2]string{}
			operations := [2]string{}
			for i, engine := range engines {
				if engine == "claude" {
					thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
					if err != nil {
						t.Fatal(err)
					}
					ids[i] = thread.ID
					if _, err := db.QueueClaudeTurn(ctx, thread, "parallel task", "sonnet", "default", ""); err != nil {
						t.Fatal(err)
					}
				} else {
					thread, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
					if err != nil {
						t.Fatal(err)
					}
					ids[i] = thread.ID
					if _, err := db.QueueGeminiTurn(ctx, thread, "parallel task", "auto", "default"); err != nil {
						t.Fatal(err)
					}
				}
				if err := gateway.flush(stream, server.ID); err != nil {
					t.Fatal(err)
				}
				select {
				case command := <-stream.sent:
					operations[i] = command.OperationID
					if command.Kind != engine+".turn.start" {
						t.Fatalf("wrong turn dispatched: %+v", command)
					}
				default:
					t.Fatal("a running session blocked another session's delivery")
				}
				sessionID := "550e8400-e29b-41d4-a716-446655440000"
				if i == 1 {
					sessionID = "550e8400-e29b-41d4-a716-446655440001"
				}
				payload, _ := json.Marshal(map[string]string{"operation_id": operations[i], "session_id": sessionID})
				raw, _ := json.Marshal(protocol.StreamEvent{StreamID: ids[i], Kind: engine + ".init", Payload: payload})
				if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: engine + "_event", PayloadJSON: raw}); err != nil {
					t.Fatal(err)
				}
			}
			assertStatus := func(index int, want string) {
				t.Helper()
				var status string
				if err := db.DB.GetContext(ctx, &status, db.Q("SELECT status FROM "+engines[index]+"_threads WHERE id=?"), ids[index]); err != nil || status != want {
					t.Fatalf("session %d: status=%s want=%s err=%v", index, status, want, err)
				}
			}
			assertStatus(0, "running")
			assertStatus(1, "running")
			// Completion for the first process must leave the second one running.
			raw, _ := json.Marshal(protocol.OperationResult{OperationID: operations[0], Status: "cancelled"})
			if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: raw}); err != nil {
				t.Fatal(err)
			}
			assertStatus(0, "interrupted")
			assertStatus(1, "running")
			if busy, err := db.HasActiveWorkspaceWriteOperation(ctx, workspaces[0].ID); err != nil || !busy {
				t.Fatalf("the remaining session lost its reservation: %v %v", busy, err)
			}
		})
	}
}
