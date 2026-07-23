package gitrepository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageUnstageDiscardAndCommitChanges(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runWriteGit(t, repository, "init", "-b", "main")
	runWriteGit(t, repository, "config", "user.name", "Wio Test")
	runWriteGit(t, repository, "config", "user.email", "wio@example.com")
	writeGitChangeFile(t, repository, "modified.txt", "before\n")
	writeGitChangeFile(t, repository, "deleted.txt", "delete me\n")
	runWriteGit(t, repository, "add", ".")
	runWriteGit(t, repository, "commit", "-m", "base")

	writeGitChangeFile(t, repository, "modified.txt", "after\n")
	writeGitChangeFile(t, repository, "untracked.txt", "new\n")
	if err := os.Remove(filepath.Join(repository, "deleted.txt")); err != nil {
		t.Fatal(err)
	}

	if err := Stage(context.Background(), repository, nil, true, []string{root}); err != nil {
		t.Fatal(err)
	}
	changes := changesByPath(t, repository, root)
	for _, path := range []string{"modified.txt", "deleted.txt", "untracked.txt"} {
		if !changes[path].Staged {
			t.Fatalf("expected %s to be staged: %#v", path, changes[path])
		}
	}

	if err := Unstage(context.Background(), repository, []string{"modified.txt"}, false, []string{root}); err != nil {
		t.Fatal(err)
	}
	changes = changesByPath(t, repository, root)
	if changes["modified.txt"].Staged || !changes["modified.txt"].Unstaged {
		t.Fatalf("modified file should only be unstaged: %#v", changes["modified.txt"])
	}
	if err := DiscardUnstaged(context.Background(), repository, []string{"modified.txt"}, false, []string{root}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(repository, "modified.txt"))
	if err != nil || strings.ReplaceAll(string(content), "\r\n", "\n") != "before\n" {
		t.Fatalf("tracked content was not restored: %q %v", content, err)
	}

	if err := Unstage(context.Background(), repository, nil, true, []string{root}); err != nil {
		t.Fatal(err)
	}
	if err := DiscardUnstaged(context.Background(), repository, nil, true, []string{root}); err != nil {
		t.Fatal(err)
	}
	if changes := changesByPath(t, repository, root); len(changes) != 0 {
		t.Fatalf("expected a clean workspace after discard: %#v", changes)
	}

	writeGitChangeFile(t, repository, "modified.txt", "staged-change\n")
	if err := Stage(context.Background(), repository, []string{"modified.txt"}, false, []string{root}); err != nil {
		t.Fatal(err)
	}
	if err := DiscardChanges(context.Background(), repository, []string{"modified.txt"}, false, []string{root}); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(filepath.Join(repository, "modified.txt"))
	if err != nil || strings.ReplaceAll(string(content), "\r\n", "\n") != "before\n" {
		t.Fatalf("staged tracked content was not restored: %q %v", content, err)
	}

	writeGitChangeFile(t, repository, "committed.txt", "ready\n")
	if err := Stage(context.Background(), repository, nil, true, []string{root}); err != nil {
		t.Fatal(err)
	}
	if err := CommitChanges(context.Background(), repository, "Add committed file", []string{root}); err != nil {
		t.Fatal(err)
	}
	if changes := changesByPath(t, repository, root); len(changes) != 0 {
		t.Fatalf("expected commit to leave a clean workspace: %#v", changes)
	}
	commits, err := ListCommits(context.Background(), repository, []string{root}, CommitLogOptions{Limit: 1})
	if err != nil || len(commits.Commits) != 1 || commits.Commits[0].Title != "Add committed file" {
		t.Fatalf("unexpected commit result: %#v %v", commits, err)
	}
}

func TestUnstageWorksBeforeInitialCommit(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runWriteGit(t, repository, "init", "-b", "main")
	writeGitChangeFile(t, repository, "first.txt", "first\n")
	if err := Stage(context.Background(), repository, []string{"first.txt"}, false, []string{root}); err != nil {
		t.Fatal(err)
	}
	if err := Unstage(context.Background(), repository, []string{"first.txt"}, false, []string{root}); err != nil {
		t.Fatal(err)
	}
	change := changesByPath(t, repository, root)["first.txt"]
	if change.Status != "untracked" || change.Staged || !change.Unstaged {
		t.Fatalf("unexpected unborn repository change: %#v", change)
	}
	if err := Stage(context.Background(), repository, []string{"../outside"}, false, []string{root}); err == nil {
		t.Fatal("expected an escaped change path to be rejected")
	}
}

func changesByPath(t *testing.T, repository, root string) map[string]WorkspaceChange {
	t.Helper()
	changes, err := ListWorkspaceChanges(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]WorkspaceChange, len(changes))
	for _, change := range changes {
		result[change.Path] = change
	}
	return result
}

func runWriteGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeGitChangeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
