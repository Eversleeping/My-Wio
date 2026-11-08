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

// Paths are always literal: a file named like a glob or pathspec magic must
// never select other files (discarding an untracked "*" used to delete every
// untracked file).
func TestChangeOperationsTreatPathsLiterally(t *testing.T) {
	requireGit(t)
	special := []string{"*", "?", "[ab]", ":(glob)x"}
	victims := []string{"a", "b", "x", "z"}
	ctx := context.Background()

	t.Run("discard untracked", func(t *testing.T) {
		root := t.TempDir()
		repository := literalPathRepository(t, root, nil)
		for _, name := range append(append([]string(nil), special...), victims...) {
			writeGitChangeFile(t, repository, name, "untracked\n")
		}
		remaining := append(append([]string(nil), special...), victims...)
		for _, name := range special {
			if err := DiscardUnstaged(ctx, repository, []string{name}, false, []string{root}); err != nil {
				t.Fatalf("discard %q: %v", name, err)
			}
			remaining = remaining[1:]
			if _, err := os.Lstat(filepath.Join(repository, name)); !os.IsNotExist(err) {
				t.Fatalf("%q was not removed: %v", name, err)
			}
			for _, other := range remaining {
				if _, err := os.Lstat(filepath.Join(repository, other)); err != nil {
					t.Fatalf("discarding %q also removed %q: %v", name, other, err)
				}
			}
		}
	})

	t.Run("stage unstage and restore tracked", func(t *testing.T) {
		root := t.TempDir()
		all := append(append([]string(nil), special...), victims...)
		repository := literalPathRepository(t, root, all)
		for _, name := range all {
			writeGitChangeFile(t, repository, name, "changed\n")
		}
		for _, name := range special {
			if err := Stage(ctx, repository, []string{name}, false, []string{root}); err != nil {
				t.Fatalf("stage %q: %v", name, err)
			}
			changes := changesByPath(t, repository, root)
			for _, other := range all {
				if staged := changes[other].Staged; staged != (other == name) {
					t.Fatalf("after staging %q, %q staged=%v", name, other, staged)
				}
			}
			if err := Unstage(ctx, repository, []string{name}, false, []string{root}); err != nil {
				t.Fatalf("unstage %q: %v", name, err)
			}
			for path, change := range changesByPath(t, repository, root) {
				if change.Staged {
					t.Fatalf("after unstaging %q, %q is still staged", name, path)
				}
			}
		}
		restored := map[string]bool{}
		for _, name := range special {
			if err := DiscardUnstaged(ctx, repository, []string{name}, false, []string{root}); err != nil {
				t.Fatalf("discard %q: %v", name, err)
			}
			restored[name] = true
			for _, other := range all {
				content, err := os.ReadFile(filepath.Join(repository, other))
				want := "changed\n"
				if restored[other] {
					want = "base\n"
				}
				if err != nil || string(content) != want {
					t.Fatalf("after discarding %q, %q = %q (%v), want %q", name, other, content, err, want)
				}
			}
		}
	})

	t.Run("unstage before initial commit", func(t *testing.T) {
		root := t.TempDir()
		repository := filepath.Join(root, "repo")
		if err := os.Mkdir(repository, 0o755); err != nil {
			t.Fatal(err)
		}
		runWriteGit(t, repository, "init", "-q", "-b", "main")
		for _, name := range append(append([]string(nil), special...), victims...) {
			writeGitChangeFile(t, repository, name, "new\n")
		}
		if err := Stage(ctx, repository, nil, true, []string{root}); err != nil {
			t.Fatal(err)
		}
		if err := Unstage(ctx, repository, []string{"*"}, false, []string{root}); err != nil {
			t.Fatal(err)
		}
		for path, change := range changesByPath(t, repository, root) {
			if change.Staged == (path == "*") {
				t.Fatalf("unstaging \"*\" before the first commit affected %q: %#v", path, change)
			}
		}
	})
}

func literalPathRepository(t *testing.T, root string, tracked []string) string {
	t.Helper()
	repository := filepath.Join(root, "repo")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runWriteGit(t, repository, "init", "-q", "-b", "main")
	runWriteGit(t, repository, "config", "user.name", "Wio Test")
	runWriteGit(t, repository, "config", "user.email", "wio@example.com")
	writeGitChangeFile(t, repository, "base.txt", "base\n")
	for _, name := range tracked {
		writeGitChangeFile(t, repository, name, "base\n")
	}
	runWriteGit(t, repository, "add", ".")
	runWriteGit(t, repository, "commit", "-q", "-m", "base")
	return repository
}
