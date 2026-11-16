package agentgateway

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

// Session payloads are plaintext: corrupting them to trigger decryption failure
// would destroy the business identifiers needed by completion. Exercise the
// exact completion path used by flush, retaining real queued payloads.
func failUndelivered(t *testing.T, g *Gateway, serverID, operationID string) {
	t.Helper()
	ctx := context.Background()
	op, err := g.store.Operation(ctx, operationID)
	if err != nil || op.Status != "queued" || op.DeliveredAt != nil {
		t.Fatalf("not undelivered: %+v %v", op, err)
	}
	for i := 0; i < 2; i++ {
		if err := g.handleOperationResult(ctx, serverID, protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "operation rejected before delivery"}, false); err != nil {
			t.Fatal(err)
		}
	}
	op, err = g.store.Operation(ctx, operationID)
	if err != nil || op.Status != "failed" || op.DeliveredAt != nil {
		t.Fatalf("not failed before delivery: %+v %v", op, err)
	}
}

func undeliveredWorkspace(t *testing.T) (*Gateway, store.Workspace) {
	t.Helper()
	ctx := context.Background()
	db, server := gatewayTestServer(t, "undelivered")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/undelivered", Name: "undelivered"}}}); err != nil {
		t.Fatal(err)
	}
	ws, err := db.ListWorkspaces(ctx)
	if err != nil || len(ws) != 1 {
		t.Fatalf("workspaces: %+v %v", ws, err)
	}
	return New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil))), ws[0]
}

func TestUndeliveredSessionTurnFailureAllowsRetry(t *testing.T) {
	for _, engine := range []string{"codex", "gemini", "claude"} {
		t.Run(engine, func(t *testing.T) {
			g, w := undeliveredWorkspace(t)
			ctx := context.Background()
			id := ""
			queue := func() string {
				t.Helper()
				var err error
				switch engine {
				case "codex":
					var thread store.Thread
					if id == "" {
						thread, err = g.store.CreateThread(ctx, w.ID, "retry")
						id = thread.ID
					} else {
						thread, err = g.store.Thread(ctx, id)
					}
					if err != nil {
						t.Fatal(err)
					}
					op, _, err := g.store.QueueCodexTurnRequest(ctx, thread, protocol.StartTurnCommand{ThreadID: id, Workspace: w.Path, Prompt: "keep my input"}, store.NewID())
					if err != nil {
						t.Fatal(err)
					}
					return op
				case "gemini":
					var thread store.GeminiThread
					if id == "" {
						thread, err = g.store.CreateGeminiThread(ctx, w.ID, "auto", "default")
						id = thread.ID
					} else {
						thread, err = g.store.GeminiThread(ctx, id)
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err = g.store.QueueGeminiTurn(ctx, thread, "keep my input", "auto", "default"); err != nil {
						t.Fatal(err)
					}
				case "claude":
					var thread store.ClaudeThread
					if id == "" {
						thread, err = g.store.CreateClaudeThread(ctx, w.ID, "sonnet", "default", "")
						id = thread.ID
					} else {
						thread, err = g.store.ClaudeThread(ctx, id)
					}
					if err != nil {
						t.Fatal(err)
					}
					if _, err = g.store.QueueClaudeTurn(ctx, thread, "keep my input", "sonnet", "default", ""); err != nil {
						t.Fatal(err)
					}
				}
				var op string
				if err := g.store.DB.GetContext(ctx, &op, g.store.Q("SELECT operation_id FROM "+engine+"_threads WHERE id=?"), id); err != nil {
					t.Fatal(err)
				}
				return op
			}
			op := queue()
			failUndelivered(t, g, w.ServerID, op)
			var status string
			if err := g.store.DB.GetContext(ctx, &status, g.store.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), id); err != nil || status != "failed" {
				t.Fatalf("thread status=%s err=%v", status, err)
			}
			events, err := g.store.RecentEvents(ctx, id, 20)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.Kind == "user.message" || event.Kind == engine+".user.message" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("user message lost or duplicated: %+v", events)
			}
			if busy, err := g.store.HasActiveWorkspaceWriteOperation(ctx, w.ID); err != nil || busy {
				t.Fatalf("reservation leaked: %v %v", busy, err)
			}
			if retry := queue(); retry == op || retry == "" {
				t.Fatalf("retry did not create a fresh operation: %s", retry)
			}
		})
	}
}

func TestUndeliveredSteerPreservesRunningTurn(t *testing.T) {
	for _, engine := range []string{"codex", "gemini", "claude"} {
		t.Run(engine, func(t *testing.T) {
			g, w := undeliveredWorkspace(t)
			ctx := context.Background()
			var id string
			var command any
			var eventKind string
			switch engine {
			case "codex":
				thread, err := g.store.CreateThread(ctx, w.ID, "running")
				if err != nil {
					t.Fatal(err)
				}
				id = thread.ID
				command = protocol.SteerTurnCommand{ThreadID: id, CodexThread: "native", ExpectedTurnID: "active", Prompt: "correction"}
				eventKind = protocol.EventCodexSteerFailed
			case "gemini":
				thread, err := g.store.CreateGeminiThread(ctx, w.ID, "auto", "default")
				if err != nil {
					t.Fatal(err)
				}
				id = thread.ID
				command = protocol.GeminiSteerCommand{ThreadID: id, OperationID: "active", RequestID: "steer", Prompt: "correction"}
				eventKind = protocol.EventGeminiSteerRejected
			case "claude":
				thread, err := g.store.CreateClaudeThread(ctx, w.ID, "sonnet", "default", "")
				if err != nil {
					t.Fatal(err)
				}
				id = thread.ID
				command = protocol.ClaudeSteerCommand{ThreadID: id, OperationID: "active", RequestID: "steer", Prompt: "correction"}
				eventKind = protocol.EventClaudeSteerRejected
			}
			if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE "+engine+"_threads SET status='running' WHERE id=?"), id); err != nil {
				t.Fatal(err)
			}
			op, err := g.store.QueueResourceOperation(ctx, w.ServerID, engine+".turn.steer", command, "steer", store.OperationResource{WorkspaceID: w.ID}, false)
			if err != nil {
				t.Fatal(err)
			}
			failUndelivered(t, g, w.ServerID, op)
			var status string
			if err := g.store.DB.GetContext(ctx, &status, g.store.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), id); err != nil || status != "running" {
				t.Fatalf("running task changed: %s %v", status, err)
			}
			events, err := g.store.RecentEvents(ctx, id, 20)
			if err != nil || len(events) != 1 || events[0].Kind != eventKind {
				t.Fatalf("rejection not visible exactly once: %+v %v", events, err)
			}
		})
	}
}

func TestUndeliveredForkReleasesSourceAndCreatesNoTarget(t *testing.T) {
	for _, engine := range []string{"codex", "gemini", "claude"} {
		for _, worktree := range []bool{false, true} {
			name := engine
			if worktree {
				name += "/worktree"
			}
			t.Run(name, func(t *testing.T) {
				g, w := undeliveredWorkspace(t)
				ctx := context.Background()
				var source, target, op string
				queue := func() {
					t.Helper()
					var tree *protocol.GitWorktreeCreateCommand
					if worktree {
						tree = &protocol.GitWorktreeCreateCommand{SourcePath: w.Path, TargetPath: "/srv/fork", TargetWorkspaceID: "target-workspace"}
					}
					var err error
					switch engine {
					case "codex":
						if source == "" {
							thread, e := g.store.CreateThread(ctx, w.ID, "source")
							if e != nil {
								t.Fatal(e)
							}
							source = thread.ID
						}
						target = store.NewID()
						fork := protocol.ForkThreadCommand{SourceThreadID: source, TargetThreadID: target, CodexThread: "native", WorkspaceID: w.ID, Workspace: w.Path}
						kind, command := protocol.OpCodexThreadFork, any(fork)
						if tree != nil {
							tree.SourceThreadID, tree.TargetThreadID, tree.CodexThread = source, target, "native"
							kind, command = protocol.OpGitWorktreeCreate, tree
						}
						op, err = g.store.QueueResourceOperation(ctx, w.ServerID, kind, command, store.NewID(), store.OperationResource{WorkspaceID: w.ID}, true)
					case "gemini":
						if source == "" {
							thread, e := g.store.CreateGeminiThread(ctx, w.ID, "auto", "default")
							if e != nil {
								t.Fatal(e)
							}
							source = thread.ID
							_, err = g.store.DB.ExecContext(ctx, g.store.Q("UPDATE gemini_threads SET session_id='native' WHERE id=?"), source)
							if err != nil {
								t.Fatal(err)
							}
						}
						thread, e := g.store.GeminiThread(ctx, source)
						if e != nil {
							t.Fatal(e)
						}
						op, target, err = g.store.QueueGeminiFork(ctx, thread, "", tree)
					case "claude":
						if source == "" {
							thread, e := g.store.CreateClaudeThread(ctx, w.ID, "sonnet", "default", "")
							if e != nil {
								t.Fatal(e)
							}
							source = thread.ID
							_, err = g.store.DB.ExecContext(ctx, g.store.Q("UPDATE claude_threads SET session_id='native' WHERE id=?"), source)
							if err != nil {
								t.Fatal(err)
							}
						}
						thread, e := g.store.ClaudeThread(ctx, source)
						if e != nil {
							t.Fatal(e)
						}
						op, target, err = g.store.QueueClaudeFork(ctx, thread, tree)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				queue()
				failUndelivered(t, g, w.ServerID, op)
				var status string
				if err := g.store.DB.GetContext(ctx, &status, g.store.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), source); err != nil || status != "idle" {
					t.Fatalf("source locked: %s %v", status, err)
				}
				var count int
				if err := g.store.DB.GetContext(ctx, &count, g.store.Q("SELECT COUNT(*) FROM "+engine+"_threads WHERE id=?"), target); err != nil || count != 0 {
					t.Fatalf("phantom fork: %d %v", count, err)
				}
				if err := g.store.DB.GetContext(ctx, &count, g.store.Q("SELECT COUNT(*) FROM workspaces WHERE id='target-workspace'")); err != nil || count != 0 {
					t.Fatalf("phantom worktree: %d %v", count, err)
				}
				if busy, err := g.store.HasActiveWorkspaceWriteOperation(ctx, w.ID); err != nil || busy {
					t.Fatalf("fork reservation leaked: %v %v", busy, err)
				}
				queue()
			})
		}
	}
}

func TestUndeliveredWorkspaceLifecyclePreservesReadyState(t *testing.T) {
	for _, action := range []string{"move", "copy", "delete"} {
		t.Run(action, func(t *testing.T) {
			g, w := undeliveredWorkspace(t)
			ctx := context.Background()
			if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE workspaces SET management_mode='managed',status='ready' WHERE id=?"), w.ID); err != nil {
				t.Fatal(err)
			}
			w.ManagementMode = "managed"
			command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: w.ID, ProjectID: w.ProjectID, SourcePath: w.Path, Action: action, TargetPath: "/srv/target", TargetWorkspaceID: "target"}
			op, err := g.store.QueueWorkspaceLifecycle(ctx, w, command)
			if err != nil {
				t.Fatal(err)
			}
			failUndelivered(t, g, w.ServerID, op)
			current, err := g.store.Workspace(ctx, w.ID)
			if err != nil || current.Status != "ready" || current.Path != w.Path {
				t.Fatalf("unexecuted workspace changed: %+v %v", current, err)
			}
			if _, err := g.store.QueueWorkspaceLifecycle(ctx, current, command); err != nil {
				t.Fatalf("retry blocked: %v", err)
			}
		})
	}
}

func TestUndeliveredGitRefreshCanRetry(t *testing.T) {
	for _, kind := range []string{protocol.OpGitWorkspaceInspect, protocol.OpGitWorkspaceWrite} {
		t.Run(kind, func(t *testing.T) {
			g, w := undeliveredWorkspace(t)
			ctx := context.Background()
			if err := g.store.BeginWorkspaceGitRefresh(ctx, w.ID); err != nil {
				t.Fatal(err)
			}
			command := any(protocol.GitWorkspaceInspectCommand{WorkspaceID: w.ID, Path: w.Path})
			if kind == protocol.OpGitWorkspaceWrite {
				command = protocol.GitWorkspaceWriteCommand{WorkspaceID: w.ID, Path: w.Path, Action: "commit"}
			}
			op, err := g.store.QueueResourceOperation(ctx, w.ServerID, kind, command, "git", store.OperationResource{WorkspaceID: w.ID}, kind == protocol.OpGitWorkspaceWrite)
			if err != nil {
				t.Fatal(err)
			}
			failUndelivered(t, g, w.ServerID, op)
			snapshot, err := g.store.WorkspaceGitSnapshot(ctx, w.ID)
			if err != nil || snapshot.Status != "failed" || snapshot.Error == "" {
				t.Fatalf("refresh stuck: %+v %v", snapshot, err)
			}
			if err := g.store.BeginWorkspaceGitRefresh(ctx, w.ID); err != nil {
				t.Fatalf("refresh retry: %v", err)
			}
		})
	}
}

func TestUndeliveredWorkspaceReadsAndWriteReleaseState(t *testing.T) {
	for _, kind := range []string{protocol.OpWorkspaceFiles, protocol.OpWorkspaceChanges, protocol.OpWorkspaceFilePreview, protocol.OpWorkspaceDiffPreview, protocol.OpWorkspaceFileWrite} {
		t.Run(kind, func(t *testing.T) {
			g, w := undeliveredWorkspace(t)
			ctx := context.Background()
			var command any
			var begin func() error
			var status func() (string, error)
			switch kind {
			case protocol.OpWorkspaceFiles:
				command = protocol.WorkspaceFilesCommand{WorkspaceID: w.ID, Path: w.Path}
				begin = func() error { return g.store.BeginWorkspaceFileScan(ctx, w.ID) }
				status = func() (string, error) { s, e := g.store.WorkspaceFileSnapshot(ctx, w.ID); return s.Status, e }
			case protocol.OpWorkspaceChanges:
				command = protocol.WorkspaceChangesCommand{WorkspaceID: w.ID, Path: w.Path}
				begin = func() error { return g.store.BeginWorkspaceChangeScan(ctx, w.ID) }
				status = func() (string, error) { s, e := g.store.WorkspaceChangeSnapshot(ctx, w.ID); return s.Status, e }
			case protocol.OpWorkspaceFilePreview:
				command = protocol.WorkspaceFilePreviewCommand{WorkspaceID: w.ID, Root: w.Path, Path: "README.md"}
				begin = func() error { return g.store.BeginWorkspaceFilePreview(ctx, w.ID, "README.md") }
				status = func() (string, error) {
					s, e := g.store.WorkspaceFilePreview(ctx, w.ID, "README.md")
					return s.Status, e
				}
			case protocol.OpWorkspaceDiffPreview:
				command = protocol.WorkspaceDiffCommand{WorkspaceID: w.ID, Root: w.Path, Path: "README.md"}
				begin = func() error { return g.store.BeginWorkspaceDiffPreview(ctx, w.ID, "README.md") }
				status = func() (string, error) {
					s, e := g.store.WorkspaceDiffPreview(ctx, w.ID, "README.md")
					return s.Status, e
				}
			case protocol.OpWorkspaceFileWrite:
				command = protocol.WorkspaceFileWriteCommand{WorkspaceID: w.ID, Root: w.Path, Path: "README.md", Content: []byte("update")}
			}
			if begin != nil {
				if err := begin(); err != nil {
					t.Fatal(err)
				}
			}
			op, err := g.store.QueueResourceOperation(ctx, w.ServerID, kind, command, "read-or-write", store.OperationResource{WorkspaceID: w.ID}, kind == protocol.OpWorkspaceFileWrite)
			if err != nil {
				t.Fatal(err)
			}
			failUndelivered(t, g, w.ServerID, op)
			if status != nil {
				s, e := status()
				if e != nil || s != "failed" {
					t.Fatalf("request stuck: %s %v", s, e)
				}
				if err := begin(); err != nil {
					t.Fatalf("retry blocked: %v", err)
				}
			} else {
				// An unexecuted write must not schedule success-side file/change scans.
				pending, err := g.store.PendingOperations(ctx, w.ServerID)
				if err != nil || len(pending) != 0 {
					t.Fatalf("unexpected refresh work: %+v %v", pending, err)
				}
			}
			if busy, err := g.store.HasActiveWorkspaceWriteOperation(ctx, w.ID); err != nil || busy {
				t.Fatalf("write lock leaked: %v %v", busy, err)
			}
		})
	}
}
