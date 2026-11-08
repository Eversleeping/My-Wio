package gitworktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCreateAndRemove(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "repo")
	runGit(t, root, "init", source)
	runGit(t, source, "config", "user.email", "test@example.com")
	runGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")
	target := filepath.Join(root, "repo-feature")
	result, err := Create(context.Background(), protocol.GitWorktreeCreateCommand{SourcePath: source, TargetPath: target, Branch: "feature/test"}, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, statErr := os.Stat(target)
	resultInfo, resultStatErr := os.Stat(result.Path)
	if statErr != nil || resultStatErr != nil || !os.SameFile(targetInfo, resultInfo) || result.Branch != "feature/test" || result.CommitSHA == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(target, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), protocol.GitWorktreeCreateCommand{SourcePath: source, TargetPath: filepath.Join(root, "other"), Branch: "feature/test"}, []string{root}); err == nil {
		t.Fatal("expected occupied branch rejection")
	}
	if err := Remove(context.Background(), source, target, result.Branch, []string{root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if err := exec.Command("git", "-C", source, "show-ref", "--verify", "refs/heads/feature/test").Run(); err == nil {
		t.Fatal("worktree branch still exists after cleanup")
	}
}

// TestRemoveIsRetryable covers cleanups that are retried after an earlier
// attempt (or somebody else) already removed part of the worktree: the
// branch must end up deleted and the retry must succeed.
func TestRemoveIsRetryable(t *testing.T) {
	scenarios := map[string]func(t *testing.T, source, target string){
		"removed twice": func(t *testing.T, source, target string) {
			if err := Remove(context.Background(), source, target, "feature/retry", []string{filepath.Dir(source)}); err != nil {
				t.Fatal(err)
			}
		},
		"directory deleted manually": func(t *testing.T, source, target string) {
			if err := os.RemoveAll(target); err != nil {
				t.Fatal(err)
			}
		},
		"worktree removed but branch left behind": func(t *testing.T, source, target string) {
			runGit(t, source, "worktree", "remove", "--force", target)
		},
		"administrative entry pruned but directory left behind": func(t *testing.T, source, target string) {
			runGit(t, source, "worktree", "remove", "--force", target)
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range scenarios {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := newRepository(t, root)
			target := filepath.Join(root, "repo-retry")
			result, err := Create(context.Background(), protocol.GitWorktreeCreateCommand{SourcePath: source, TargetPath: target, Branch: "feature/retry"}, []string{root})
			if err != nil {
				t.Fatal(err)
			}
			prepare(t, source, result.Path)
			if err := Remove(context.Background(), source, result.Path, result.Branch, []string{root}); err != nil {
				t.Fatalf("retried cleanup failed: %v", err)
			}
			if err := exec.Command("git", "-C", source, "show-ref", "--verify", "--quiet", "refs/heads/feature/retry").Run(); err == nil {
				t.Fatal("worktree branch still exists after cleanup")
			}
			if output, err := exec.Command("git", "-C", source, "worktree", "list", "--porcelain").Output(); err != nil || strings.Contains(string(output), "repo-retry") {
				t.Fatalf("stale worktree entry remains: %s %v", output, err)
			}
			// A further retry is a no-op.
			if err := Remove(context.Background(), source, result.Path, result.Branch, []string{root}); err != nil {
				t.Fatalf("repeated cleanup failed: %v", err)
			}
		})
	}
}

func TestRemoveReportsBranchStillCheckedOut(t *testing.T) {
	root := t.TempDir()
	source := newRepository(t, root)
	target := filepath.Join(root, "repo-missing")
	current, err := exec.Command("git", "-C", source, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	// The source checkout's own branch cannot be deleted: that failure must
	// be reported rather than mistaken for an already deleted branch.
	if err := Remove(context.Background(), source, target, strings.TrimSpace(string(current)), []string{root}); err == nil {
		t.Fatal("deleting the checked-out branch was reported as success")
	}
}

func TestCreateRejectsUnsafeInput(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	_, err := Create(context.Background(), protocol.GitWorktreeCreateCommand{SourcePath: root, TargetPath: outside, Branch: "feature/test"}, []string{root})
	if err == nil {
		t.Fatal("expected path outside configured roots to fail")
	}
	_, err = Create(context.Background(), protocol.GitWorktreeCreateCommand{SourcePath: root, TargetPath: filepath.Join(root, "target"), Branch: "bad branch"}, []string{root})
	if err == nil {
		t.Fatal("expected invalid branch to fail")
	}
}

func TestCreateRejectsSourceAndTargetAncestors(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "repo")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Join(source, "nested"), root} {
		_, err := Create(context.Background(), protocol.GitWorktreeCreateCommand{SourcePath: source, TargetPath: target, Branch: "feature/test"}, []string{root})
		if err == nil {
			t.Fatalf("expected ancestor relationship to fail: %s", target)
		}
	}
}

func newRepository(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "repo")
	runGit(t, root, "init", source)
	runGit(t, source, "config", "user.email", "test@example.com")
	runGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "initial")
	return source
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
