package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWorktreeDeletionCleansSessionsAfterSuccess(t *testing.T) {
	assertWorktreeDeletionCleansSessions(t, testStore(t))
}

// Also run against PostgreSQL, where workspace writes use row locks.
func assertWorktreeDeletionCleansSessions(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, NewID(), NewID())
	project, err := db.CreateProject(ctx, "worktree deletion "+NewID(), "")
	if err != nil {
		t.Fatal(err)
	}
	parentID := insertDeletionWorkspace(t, db, project.ID, server.ID, "/srv/"+NewID(), "managed", "primary", 0)
	parent, err := db.CreateThread(ctx, parentID, "source survives")
	if err != nil {
		t.Fatal(err)
	}
	worktreeID := insertDeletionWorkspace(t, db, project.ID, server.ID, "/srv/"+NewID(), "managed", "worktree", 0)
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE workspaces SET parent_workspace_id=? WHERE id=?"), parentID, worktreeID); err != nil {
		t.Fatal(err)
	}
	workspace, _ := db.Workspace(ctx, worktreeID)
	codex, err := db.CreateThread(ctx, worktreeID, "worktree session")
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := db.CreateGeminiThread(ctx, worktreeID, "auto", "default")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := db.CreateClaudeThread(ctx, worktreeID, "sonnet", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, codex.ID, gemini.ID, claude.ID} {
		if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,1,'test.message',CURRENT_TIMESTAMP,'{}')"), NewID(), id); err != nil {
			t.Fatal(err)
		}
	}
	createTestScheduledTask(t, db, "codex", codex.ID, time.Now().UTC())
	createTestScheduledTask(t, db, "gemini", gemini.ID, time.Now().UTC())
	createTestScheduledTask(t, db, "claude", claude.ID, time.Now().UTC())
	for _, table := range []string{"session_subscriptions", "session_tool_waits"} {
		columns, values := "target_key,source_key,grant_version", "?,?,1"
		if table == "session_tool_waits" {
			columns, values = "target_key,source_key,expires_at", "?,?,CURRENT_TIMESTAMP"
		}
		for _, pair := range [][2]string{{"codex:" + codex.ID, "codex:" + parent.ID}, {"codex:" + parent.ID, "claude:" + claude.ID}} {
			if _, err := db.DB.ExecContext(ctx, db.Q("INSERT INTO "+table+"("+columns+") VALUES("+values+")"), pair[0], pair[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan, err := db.WorkspaceDeletionPlan(ctx, worktreeID, false)
	if err != nil || !plan.CanDeleteFiles || plan.CanRemoveRecord || plan.ThreadCount != 3 {
		t.Fatalf("unexpected worktree plan: %+v %v", plan, err)
	}
	if err := db.RemoveWorkspaceRecord(ctx, worktreeID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("metadata removal bypassed sessions: %v", err)
	}
	command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: worktreeID, ProjectID: project.ID, SourcePath: workspace.Path, WorkspaceKind: "worktree", Action: "delete"}
	// The preview must not authorize a deletion after a new session starts.
	for _, table := range []string{"codex_threads", "gemini_threads", "claude_threads"} {
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE "+table+" SET status='running' WHERE workspace_id=?"), worktreeID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.QueueWorkspaceLifecycle(ctx, workspace, command); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("active %s was not blocked: %v", table, err)
		}
		if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE "+table+" SET status='idle' WHERE workspace_id=?"), worktreeID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE workspaces SET dirty=1 WHERE id=?"), worktreeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueWorkspaceLifecycle(ctx, workspace, command); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("dirty worktree was not blocked: %v", err)
	}
	command.Force = true
	operationID, err := db.QueueWorkspaceLifecycle(ctx, workspace, command)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := db.Operation(ctx, operationID)
	if err := db.FailWorkspaceLifecycle(ctx, operation, command, protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "git refused deletion"}, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Thread(ctx, codex.ID); err != nil {
		t.Fatalf("failure removed session: %v", err)
	}
	if _, err := db.Workspace(ctx, worktreeID); err != nil {
		t.Fatalf("failure removed workspace: %v", err)
	}
	operationID, err = db.QueueWorkspaceLifecycle(ctx, workspace, command)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ = db.Operation(ctx, operationID)
	result := protocol.GitWorkspaceLifecycleResult{WorkspaceID: worktreeID, Action: "delete", SourcePath: workspace.Path}
	data, _ := json.Marshal(result)
	completed := protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: data}
	if err := db.CompleteWorkspaceLifecycle(ctx, operation, command, result, completed); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"workspaces", "codex_threads", "gemini_threads", "claude_threads"} {
		column := "workspace_id"
		if table == "workspaces" {
			column = "id"
		}
		var count int
		if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?"), worktreeID); err != nil || count != 0 {
			t.Fatalf("%s remains: %d %v", table, count, err)
		}
	}
	for _, table := range []string{"events", "scheduled_tasks"} {
		column := "stream_id"
		if table == "scheduled_tasks" {
			column = "thread_id"
		}
		var count int
		if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM "+table+" WHERE "+column+" IN (?,?,?)"), codex.ID, gemini.ID, claude.ID); err != nil || count != 0 {
			t.Fatalf("%s remains: %d %v", table, count, err)
		}
	}
	for _, table := range []string{"session_subscriptions", "session_tool_waits"} {
		var count int
		if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM "+table+" WHERE target_key=? OR source_key=?"), "codex:"+codex.ID, "claude:"+claude.ID); err != nil || count != 0 {
			t.Fatalf("%s remains: %d %v", table, count, err)
		}
	}
	if _, err := db.Thread(ctx, parent.ID); err != nil {
		t.Fatalf("source session removed: %v", err)
	}
	var count int
	if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM events WHERE stream_id=?"), parent.ID); err != nil || count != 1 {
		t.Fatalf("source history removed: %d %v", count, err)
	}
	// Stale inventory cannot recreate the deleted workspace record.
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: workspace.Path, Name: "stale worktree"}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM workspaces WHERE server_id=? AND path=?"), server.ID, workspace.Path); err != nil || count != 0 {
		t.Fatalf("deleted worktree rediscovered: %d %v", count, err)
	}
	if err := db.CompleteWorkspaceLifecycle(ctx, operation, command, result, completed); !errors.Is(err, ErrOperationNotActive) {
		t.Fatalf("stale completion was accepted: %v", err)
	}
}
