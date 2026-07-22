package store

import (
	"context"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestWorkspaceGitSnapshotPersistsStructuredDataAndWorkspaceSummary(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	server := createOperationTestServer(t, database, "git-snapshot-server", "git-snapshot-token")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/git-snapshot", Name: "snapshot", Branch: "main", CommitSHA: "old", Dirty: false}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	result := protocol.GitWorkspaceInspectResult{WorkspaceID: workspaces[0].ID, Status: protocol.GitStatus{Branch: "feature/read-only", Head: "abc123", Dirty: true}, Changes: []protocol.WorkspaceChange{{Path: "new.txt", Status: "untracked", Unstaged: true}}, Branches: []protocol.GitBranch{{Name: "feature/read-only", FullName: "refs/heads/feature/read-only", Kind: "local", Current: true}}, Commits: []protocol.GitCommit{{SHA: "abc123", Title: "Inspect"}}}
	if err := database.SaveWorkspaceGitSnapshot(ctx, workspaces[0].ID, result); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.WorkspaceGitSnapshot(ctx, workspaces[0].ID)
	if err != nil || snapshot.Status != "succeeded" || snapshot.Data.Status.Head != "abc123" || len(snapshot.Data.Changes) != 1 || snapshot.Data.Changes[0].Path != "new.txt" || len(snapshot.Data.Branches) != 1 {
		t.Fatalf("unexpected snapshot: %#v %v", snapshot, err)
	}
	updated, err := database.Workspace(ctx, workspaces[0].ID)
	if err != nil || updated.Branch != "feature/read-only" || updated.CommitSHA != "abc123" || updated.Dirty != 1 || updated.GitError != "" {
		t.Fatalf("workspace summary was not updated: %#v %v", updated, err)
	}
}
