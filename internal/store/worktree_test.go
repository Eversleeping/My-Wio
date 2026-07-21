package store

import (
	"context"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCommitGitWorktreeAtomicallyCreatesContinuedThread(t *testing.T) {
	database, err := Open(t.TempDir() + "/worktree.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	serverID, projectID, sourceWorkspaceID, sourceThreadID := NewID(), NewID(), NewID(), NewID()
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO servers(id,name) VALUES(?,?)"), serverID, "server"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO projects(id,name) VALUES(?,?)"), projectID, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO workspaces(id,project_id,server_id,path) VALUES(?,?,?,?)"), sourceWorkspaceID, projectID, serverID, "/srv/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO codex_threads(id,workspace_id,codex_thread_id,title,last_sequence) VALUES(?,?,?,?,?)"), sourceThreadID, sourceWorkspaceID, "codex-source", "Source", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), sourceThreadID, 1, "message", time.Now().UTC(), `{}`); err != nil {
		t.Fatal(err)
	}
	command := protocol.GitWorktreeCreateCommand{SourceWorkspaceID: sourceWorkspaceID, TargetWorkspaceID: NewID(), ProjectID: projectID, SourcePath: "/srv/repo", TargetPath: "/srv/repo-feature", Branch: "feature/test", SourceThreadID: sourceThreadID, TargetThreadID: NewID(), Title: "Source (continued)"}
	result := protocol.GitWorktreeCreateResult{Path: command.TargetPath, Branch: command.Branch, CommitSHA: "abc123", CodexThread: "codex-target"}
	if err := database.CommitGitWorktree(ctx, command, result); err != nil {
		t.Fatal(err)
	}
	workspace, err := database.Workspace(ctx, command.TargetWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.ManagementMode != "managed" || workspace.Kind != "worktree" || workspace.ParentWorkspaceID == nil || *workspace.ParentWorkspaceID != sourceWorkspaceID {
		t.Fatalf("unexpected workspace: %#v", workspace)
	}
	thread, err := database.Thread(ctx, command.TargetThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.WorkspaceID != command.TargetWorkspaceID || thread.CodexThreadID != "codex-target" {
		t.Fatalf("unexpected continued thread: %#v", thread)
	}
	var events int
	if err := database.DB.GetContext(ctx, &events, database.Q("SELECT COUNT(*) FROM events WHERE stream_id=?"), command.TargetThreadID); err != nil || events != 1 {
		t.Fatalf("events not copied: %d %v", events, err)
	}
}

func TestCommitGitWorktreeRejectsMismatchedResultWithoutRows(t *testing.T) {
	database, err := Open(t.TempDir() + "/worktree.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	serverID, projectID, sourceID := NewID(), NewID(), NewID()
	_, _ = database.DB.ExecContext(ctx, database.Q("INSERT INTO servers(id,name) VALUES(?,?)"), serverID, "server")
	_, _ = database.DB.ExecContext(ctx, database.Q("INSERT INTO projects(id,name) VALUES(?,?)"), projectID, "project")
	_, _ = database.DB.ExecContext(ctx, database.Q("INSERT INTO workspaces(id,project_id,server_id,path) VALUES(?,?,?,?)"), sourceID, projectID, serverID, "/srv/repo")
	command := protocol.GitWorktreeCreateCommand{SourceWorkspaceID: sourceID, TargetWorkspaceID: NewID(), ProjectID: projectID, TargetPath: "/srv/expected", Branch: "feature/test"}
	if err := database.CommitGitWorktree(ctx, command, protocol.GitWorktreeCreateResult{Path: "/srv/wrong", Branch: "wrong", CommitSHA: "abc"}); err == nil {
		t.Fatal("expected mismatched result to fail")
	}
	var count int
	if err := database.DB.GetContext(ctx, &count, database.Q("SELECT COUNT(*) FROM workspaces WHERE id=?"), command.TargetWorkspaceID); err != nil || count != 0 {
		t.Fatalf("partial workspace persisted: %d %v", count, err)
	}
}
