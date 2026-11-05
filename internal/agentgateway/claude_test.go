package agentgateway

import (
	"context"
	"encoding/json"
	"github.com/wio-platform/wio/internal/store"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
)

func TestClaudeSessionStreamsResumesAndSharesWorkspaceLock(t *testing.T) {
	db, server := gatewayTestServer(t, "claude")
	ctx := context.Background()
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.90"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude", Name: "claude"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %+v %v", workspaces, err)
	}
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "first", "sonnet", "acceptEdits", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	claimed, err := db.ClaimGeminiDelivery(ctx, thread.OperationID)
	if err != nil || !claimed {
		t.Fatalf("delivery claim: %v %v", claimed, err)
	}
	claimed, err = db.ClaimGeminiDelivery(ctx, thread.OperationID)
	if err != nil || claimed {
		t.Fatalf("duplicate delivery: %v %v", claimed, err)
	}
	other, err := db.CreateGeminiThread(ctx, workspaces[0].ID, "auto", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueGeminiTurn(ctx, other, "conflict", "auto", "default"); err == nil {
		t.Fatal("concurrent workspace write accepted")
	}
	other, _ = db.GeminiThread(ctx, other.ID)
	events, _ := db.Events(ctx, other.ID, 0, 500)
	if other.Status != "idle" || len(events) != 0 {
		t.Fatalf("conflicting claim was not rolled back: %+v %+v", other, events)
	}
	plan, err := db.WorkspaceDeletionPlan(ctx, workspaces[0].ID, false)
	if err != nil || plan.ThreadCount != 2 || plan.CanRemoveRecord {
		t.Fatalf("deletion plan: %+v %v", plan, err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	native := "550e8400-e29b-41d4-a716-446655440000"
	payload, _ := json.Marshal(map[string]string{"operation_id": thread.OperationID, "session_id": native})
	raw, _ := json.Marshal(protocol.StreamEvent{EventID: "init", StreamID: thread.ID, Kind: "claude.init", Payload: payload})
	if err := g.handle(ctx, "foreign", &protocol.AgentEnvelope{Kind: "claude_event", PayloadJSON: raw}); err == nil {
		t.Fatal("foreign server event accepted")
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "claude_event", PayloadJSON: raw}); err != nil {
		t.Fatal(err)
	}
	current, _ := db.ClaudeThread(ctx, thread.ID)
	if current.SessionID != native || current.Status != "running" {
		t.Fatalf("init: %+v", current)
	}
	result, _ := json.Marshal(protocol.OperationResult{OperationID: thread.OperationID, Status: "succeeded"})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.ClaudeThread(ctx, thread.ID)
	if current.Status != "idle" {
		t.Fatalf("completion: %+v", current)
	}
	if _, err := db.QueueClaudeTurn(ctx, current, "continue", "opus", "plan", ""); err != nil {
		t.Fatal(err)
	}
	current, _ = db.ClaudeThread(ctx, thread.ID)
	operation, _ := db.Operation(ctx, current.OperationID)
	var command protocol.ClaudeTurnCommand
	if json.Unmarshal([]byte(operation.Payload), &command) != nil || command.SessionID != native || command.Model != "opus" || command.PermissionMode != "plan" {
		t.Fatalf("resume lost settings: %+v", command)
	}
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "claude_event", PayloadJSON: raw}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.ClaudeThread(ctx, thread.ID)
	if current.Status != "queued" {
		t.Fatalf("stale event changed later turn: %+v", current)
	}
}

func TestClaudeLostOwnershipKeepsWorkspaceUntilConfirmed(t *testing.T) {
	db, server := gatewayTestServer(t, "claude-lost")
	ctx := context.Background()
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.90"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/lost", Name: "lost"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, _ := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if _, err := db.QueueClaudeTurn(ctx, thread, "first", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if _, err := db.ClaimGeminiDelivery(ctx, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := g.updateClaudeOwnership(ctx, server.ID, thread.OperationID, false); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if thread.Status != "interrupted" || !thread.RecoveryPending {
		t.Fatalf("lost ownership: %+v", thread)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "second", "sonnet", "default", ""); err == nil {
		t.Fatal("unconfirmed process allowed another turn")
	}
	if err := db.ConfirmClaudeStopped(ctx, thread.ID, "different-operation"); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	if err := db.ConfirmClaudeStopped(ctx, thread.ID, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := g.updateClaudeOwnership(ctx, server.ID, thread.OperationID, true); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if thread.Status != "interrupted" || thread.RecoveryPending {
		t.Fatalf("late probe revived confirmed task: %+v", thread)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "second", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeInterruptReceiptMustConfirmProcessExit(t *testing.T) {
	db, server := gatewayTestServer(t, "claude-stop")
	ctx := context.Background()
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.90"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/stop", Name: "stop"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	thread, _ := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if _, err := db.QueueClaudeTurn(ctx, thread, "first", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	_, _ = db.ClaimGeminiDelivery(ctx, thread.OperationID)
	if err := db.InterruptClaudeTurn(ctx, thread); err != nil {
		t.Fatal(err)
	}
	operations, _ := db.PendingOperations(ctx, server.ID)
	var interruptID string
	for _, op := range operations {
		if op.Kind == "claude.turn.interrupt" {
			interruptID = op.ID
		}
	}
	if interruptID == "" {
		t.Fatal("interrupt not queued")
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	receipt, _ := json.Marshal(protocol.GeminiInterruptResult{OperationID: thread.OperationID, Stopped: true})
	result, _ := json.Marshal(protocol.OperationResult{OperationID: interruptID, Status: "succeeded", Data: receipt})
	if err := g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
		t.Fatal(err)
	}
	thread, _ = db.ClaudeThread(ctx, thread.ID)
	if thread.Status != "interrupted" || thread.RecoveryPending {
		t.Fatalf("confirmed interrupt: %+v", thread)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "second", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCompletionDuringInterruptPublishesStoredStatus(t *testing.T) {
	db, server := gatewayTestServer(t, "claude-complete-interrupt")
	ctx := context.Background()
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/complete-interrupt", Name: "interrupt"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %+v %v", workspaces, err)
	}
	thread, err := db.CreateClaudeThread(ctx, workspaces[0].ID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueClaudeTurn(ctx, thread, "first", "sonnet", "default", ""); err != nil {
		t.Fatal(err)
	}
	thread, err = db.ClaudeThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimGeminiDelivery(ctx, thread.OperationID); err != nil || !claimed {
		t.Fatalf("delivery claim: %v %v", claimed, err)
	}
	if err := db.InterruptClaudeTurn(ctx, thread); err != nil {
		t.Fatal(err)
	}
	op, err := db.Operation(ctx, thread.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := g.completeClaudeOperation(ctx, op, protocol.OperationResult{OperationID: op.ID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	thread, err = db.ClaudeThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.Events(ctx, thread.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var completion struct {
		Status string `json:"status"`
	}
	if len(events) == 0 || events[len(events)-1].Kind != "claude.turn.completed" || json.Unmarshal(events[len(events)-1].Payload, &completion) != nil {
		t.Fatalf("missing completion event: %+v", events)
	}
	if thread.Status != "interrupted" || completion.Status != thread.Status {
		t.Fatalf("status mismatch: stored=%q event=%q", thread.Status, completion.Status)
	}
}

func TestClaudeForkCompletionAndScheduledNativeIdentity(t *testing.T) {
	for _, worktree := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-workspace", true: "worktree"}[worktree], func(t *testing.T) {
			db, server := gatewayTestServer(t, "claude-fork")
			ctx := context.Background()
			if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.2.93"}); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-fork", Name: "claude"}}}); err != nil {
				t.Fatal(err)
			}
			workspaces, _ := db.ListWorkspaces(ctx)
			w := workspaces[0]
			source, err := db.CreateClaudeThread(ctx, w.ID, "sonnet", "default", "")
			if err != nil {
				t.Fatal(err)
			}
			native := "550e8400-e29b-41d4-a716-446655440000"
			if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET session_id=? WHERE id=?"), native, source.ID); err != nil {
				t.Fatal(err)
			}
			source, _ = db.ClaudeThread(ctx, source.ID)
			var wt *protocol.GitWorktreeCreateCommand
			if worktree {
				wt = &protocol.GitWorktreeCreateCommand{SourceWorkspaceID: w.ID, TargetWorkspaceID: store.NewID(), ProjectID: w.ProjectID, SourcePath: w.Path, TargetPath: "/srv/claude-continued", Branch: "continued"}
			}
			op, target, err := db.QueueClaudeFork(ctx, source, wt)
			if err != nil {
				t.Fatal(err)
			}
			resultData, _ := json.Marshal(protocol.ClaudeForkResult{SnapshotID: target})
			if wt != nil {
				resultData, _ = json.Marshal(protocol.GitWorktreeCreateResult{Path: wt.TargetPath, Branch: wt.Branch, CommitSHA: "abc", ClaudeSnapshotID: target})
			}
			g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			result, _ := json.Marshal(protocol.OperationResult{OperationID: op, Status: "succeeded", Data: resultData})
			for i := 0; i < 2; i++ {
				if err = g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
					t.Fatal(err)
				}
			}
			source, _ = db.ClaudeThread(ctx, source.ID)
			branch, err := db.ClaudeThread(ctx, target)
			if err != nil || source.Status != "idle" || branch.ForkSnapshotID != target || branch.SessionID != "" {
				t.Fatalf("bad fork: source=%+v target=%+v %v", source, branch, err)
			}
			task, err := db.CreateScheduledTask(ctx, store.ScheduledTaskInput{AgentKind: "claude", ThreadID: target, Name: "review", Prompt: "review", Schedule: "@hourly", Timezone: "UTC", Enabled: true}, time.Now().UTC().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.QueueClaudeTurn(ctx, branch, "continue", "sonnet", "default", "")
			if err != nil {
				t.Fatal(err)
			}
			branch, _ = db.ClaudeThread(ctx, target)
			if err = db.MarkScheduledTaskRun(ctx, task.ID, "queued", "", branch.OperationID, time.Now()); err != nil {
				t.Fatal(err)
			}
			init := func(session string) error {
				payload, _ := json.Marshal(map[string]string{"operation_id": branch.OperationID, "session_id": session})
				raw, _ := json.Marshal(protocol.StreamEvent{StreamID: target, Kind: "claude.init", Payload: payload})
				return g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "claude_event", PayloadJSON: raw})
			}
			if err = init(native); err == nil {
				t.Fatal("fork reused source native ID")
			}
			independent := "550e8400-e29b-41d4-a716-446655440001"
			if err = init(independent); err != nil {
				t.Fatal(err)
			}
			saved, _ := db.ScheduledTask(ctx, task.ID)
			if saved.LastRunStatus != "running" {
				t.Fatalf("not running: %+v", saved)
			}
			result, _ = json.Marshal(protocol.OperationResult{OperationID: branch.OperationID, Status: "succeeded"})
			if err = g.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: result}); err != nil {
				t.Fatal(err)
			}
			saved, _ = db.ScheduledTask(ctx, task.ID)
			branch, _ = db.ClaudeThread(ctx, target)
			if saved.LastRunStatus != "succeeded" || branch.SessionID != independent || branch.Status != "idle" {
				t.Fatalf("completion lost: %+v %+v", saved, branch)
			}
			if _, err = db.QueueClaudeTurn(ctx, branch, "next", "sonnet", "default", ""); err != nil {
				t.Fatal(err)
			}
			branch, _ = db.ClaudeThread(ctx, target)
			operation, _ := db.Operation(ctx, branch.OperationID)
			var command protocol.ClaudeTurnCommand
			_ = json.Unmarshal([]byte(operation.Payload), &command)
			if command.SessionID != independent || command.ForkSnapshotID != "" {
				t.Fatalf("fork repeated instead of resuming: %+v", command)
			}
		})
	}
}
