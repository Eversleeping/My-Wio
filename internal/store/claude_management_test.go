package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeManagementStorage(t *testing.T) { assertClaudeManagementStorage(t, testStore(t)) }
func assertClaudeManagementStorage(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "claude-management", "claude-management")
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/claude-management", Name: "claude-management"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, _ := db.ListWorkspaces(ctx)
	var workspace Workspace
	for _, w := range workspaces {
		if w.ServerID == server.ID {
			workspace = w
		}
	}
	source, err := db.CreateClaudeThread(ctx, workspace.ID, "custom-model", "plan", "high")
	if err != nil {
		t.Fatal(err)
	}
	native := "550e8400-e29b-41d4-a716-446655440000"
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET session_id=? WHERE id=?"), native, source.ID); err != nil {
		t.Fatal(err)
	}
	source, _ = db.ClaudeThread(ctx, source.ID)
	first, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: source.ID, Kind: "claude.user.message", Payload: json.RawMessage(`{"text":"history"}`)})
	if err != nil {
		t.Fatal(err)
	}
	opID, target, err := db.QueueClaudeFork(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.QueueClaudeFork(ctx, source, nil); err == nil {
		t.Fatal("forked an active source")
	}
	if _, err = db.QueueClaudeTurn(ctx, source, "new", "sonnet", "default", ""); err == nil {
		t.Fatal("sent while snapshot was active")
	}
	op, _ := db.Operation(ctx, opID)
	var command protocol.ClaudeForkCommand
	if json.Unmarshal([]byte(op.Payload), &command) != nil {
		t.Fatal("invalid fork command")
	}
	if command.CutoffSequence != first.Sequence {
		t.Fatalf("wrong cutoff: %+v", command)
	}
	if err = db.CommitClaudeFork(ctx, command, "wrong"); err == nil {
		t.Fatal("accepted unrelated snapshot")
	}
	if err = db.CommitClaudeFork(ctx, command, target); err != nil {
		t.Fatal(err)
	}
	if err = db.CommitClaudeFork(ctx, command, target); err != nil {
		t.Fatal("duplicate completion:", err)
	}
	branch, err := db.ClaudeThread(ctx, target)
	if err != nil || branch.SessionID != "" || branch.ForkSnapshotID != target || branch.ForkSessionID != native || branch.Model != "custom-model" || branch.Effort != "high" || branch.PermissionMode != "plan" {
		t.Fatalf("bad branch: %+v %v", branch, err)
	}
	events, _ := db.Events(ctx, target, 0, 100)
	if len(events) != 1 || events[0].EventID == first.EventID {
		t.Fatal("history not independently copied")
	}
	if err = db.PinClaudeThread(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	branch, _ = db.ClaudeThread(ctx, target)
	if branch.PinnedAt == nil {
		t.Fatal("pin missing")
	}
	if err = db.PinClaudeThread(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	// Release snapshot lock, then the target starts a native fork with images.
	if err = db.CompleteOperation(ctx, protocol.OperationResult{OperationID: opID, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET status='idle' WHERE id=?"), source.ID); err != nil {
		t.Fatal(err)
	}
	image := protocol.TurnImage{DataURL: "data:image/png;base64,aGVsbG8="}
	event, err := db.QueueClaudeTurn(ctx, branch, "", "custom-model", "plan", "high", image)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		OperationID string `json:"operation_id"`
		Images      []protocol.TurnImage
	}
	_ = json.Unmarshal(event.Payload, &p)
	turnOp, _ := db.Operation(ctx, p.OperationID)
	var turn protocol.ClaudeTurnCommand
	_ = json.Unmarshal([]byte(turnOp.Payload), &turn)
	if turn.SessionID != "" || turn.ForkSnapshotID != target || turn.ForkSessionID != native || len(turn.Images) != 1 || len(p.Images) != 1 {
		t.Fatalf("fork input lost: %+v", turn)
	}
	// Archiving excludes both active source and target, and requires server scope.
	n, err := db.ArchiveClaudeProject(ctx, source.ProjectID, "other-server")
	if err != nil || n != 0 {
		t.Fatalf("cross-server archive: %d %v", n, err)
	}
	n, err = db.ArchiveClaudeProject(ctx, source.ProjectID, server.ID)
	if err != nil || n != 1 {
		t.Fatalf("archive should skip active target: %d %v", n, err)
	}
	task, err := db.CreateScheduledTask(ctx, ScheduledTaskInput{AgentKind: "claude", ThreadID: target, Name: "review", Prompt: "review", Schedule: "@hourly", Timezone: "UTC", Enabled: true, Model: "custom-model", ApprovalMode: "plan", ReasoningEffort: "high"}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if task.AgentKind != "claude" || task.ReasoningEffort != "high" {
		t.Fatalf("bad schedule: %+v", task)
	}
	if err = db.MarkScheduledTaskRun(ctx, task.ID, "queued", "", p.OperationID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkScheduledTaskByOperation(ctx, p.OperationID, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	saved, _ := db.ScheduledTask(ctx, task.ID)
	if saved.LastRunStatus != "succeeded" {
		t.Fatal("schedule completion lost")
	}
	// Also persist a worktree fork and its new workspace in one transaction.
	other, err := db.CreateClaudeThread(ctx, workspace.ID, "opus", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE claude_threads SET session_id=? WHERE id=?"), native, other.ID); err != nil {
		t.Fatal(err)
	}
	other, _ = db.ClaudeThread(ctx, other.ID)
	wt := protocol.GitWorktreeCreateCommand{SourceWorkspaceID: workspace.ID, TargetWorkspaceID: NewID(), ProjectID: workspace.ProjectID, SourcePath: workspace.Path, TargetPath: "/srv/claude-management-worktree", Branch: "claude-test"}
	// A busy workspace prevents worktree snapshots too.
	if _, _, err = db.QueueClaudeFork(ctx, other, &wt); err == nil {
		t.Fatal("fork worktree bypassed active workspace lock")
	}
	branch, _ = db.ClaudeThread(ctx, target)
	if err = db.InterruptClaudeTurn(ctx, branch); err != nil {
		t.Fatal(err)
	}
	_, workTarget, err := db.QueueClaudeFork(ctx, other, &wt)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CommitGitWorktree(ctx, wt, protocol.GitWorktreeCreateResult{Path: wt.TargetPath, Branch: wt.Branch, CommitSHA: "abc", ClaudeSnapshotID: workTarget}); err != nil {
		t.Fatal(err)
	}
	continued, err := db.ClaudeThread(ctx, workTarget)
	if err != nil || continued.Path != wt.TargetPath || continued.ForkSnapshotID != workTarget {
		t.Fatalf("worktree branch: %+v %v", continued, err)
	}
}
