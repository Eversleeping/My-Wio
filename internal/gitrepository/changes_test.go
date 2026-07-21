package gitrepository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkspaceChangesAndDiffs(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runChangesGit(t, repository, "init", "-b", "main")
	runChangesGit(t, repository, "config", "user.name", "Wio Test")
	runChangesGit(t, repository, "config", "user.email", "wio@example.com")
	writeChangesFile(t, repository, "modified.txt", "before\ncontext\n")
	writeChangesFile(t, repository, "deleted.txt", "remove me\n")
	writeChangesFile(t, repository, "renamed.txt", "rename me\n")
	runChangesGit(t, repository, "add", ".")
	runChangesGit(t, repository, "commit", "-m", "base")

	writeChangesFile(t, repository, "modified.txt", "after\ncontext\n")
	writeChangesFile(t, repository, "untracked.txt", "new line\nsecond line\n")
	if err := os.Remove(filepath.Join(repository, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	runChangesGit(t, repository, "mv", "renamed.txt", "moved.txt")

	changes, err := ListWorkspaceChanges(context.Background(), repository, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]WorkspaceChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	if byPath["modified.txt"].Status != "modified" || !byPath["modified.txt"].Unstaged {
		t.Fatalf("unexpected modified entry: %#v", byPath["modified.txt"])
	}
	if byPath["deleted.txt"].Status != "deleted" || byPath["untracked.txt"].Status != "untracked" {
		t.Fatalf("missing deleted or untracked changes: %#v", changes)
	}
	if byPath["moved.txt"].Status != "renamed" || byPath["moved.txt"].OldPath != "renamed.txt" || !byPath["moved.txt"].Staged {
		t.Fatalf("unexpected rename entry: %#v", byPath["moved.txt"])
	}

	modified, err := ReadWorkspaceDiff(context.Background(), repository, "modified.txt", "", []string{root}, MaxWorkspaceDiffBytes)
	if err != nil {
		t.Fatal(err)
	}
	if modified.Additions != 1 || modified.Deletions != 1 || modified.Content == "" {
		t.Fatalf("unexpected modified diff: %#v", modified)
	}
	untracked, err := ReadWorkspaceDiff(context.Background(), repository, "untracked.txt", "", []string{root}, MaxWorkspaceDiffBytes)
	if err != nil {
		t.Fatal(err)
	}
	if untracked.Additions != 2 || untracked.Deletions != 0 || untracked.Content == "" {
		t.Fatalf("unexpected untracked diff: %#v", untracked)
	}
	if _, err := ReadWorkspaceDiff(context.Background(), repository, "../secret", "", []string{root}, MaxWorkspaceDiffBytes); err == nil {
		t.Fatal("expected escaped diff path to be rejected")
	}
}

func runChangesGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeChangesFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
