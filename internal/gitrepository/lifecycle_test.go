package gitrepository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCloneWorkspaceFromHTTPSRemoteAndVerifyExpectedHead(t *testing.T) {
	requireGit(t)
	remoteRoot := t.TempDir()
	remote := filepath.Join(remoteRoot, "project.git")
	testGit(t, remoteRoot, "init", "--bare", remote)
	sourceRoot := t.TempDir()
	source := createRepository(t, sourceRoot, "source")
	testGit(t, source, "remote", "add", "origin", remote)
	testGit(t, source, "push", "origin", "main")
	testGit(t, remote, "update-server-info")
	head := strings.TrimSpace(testGitOutput(t, source, "rev-parse", "HEAD"))
	server := httptest.NewTLSServer(http.FileServer(http.Dir(remoteRoot)))
	t.Cleanup(server.Close)
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	managedRoot := t.TempDir()
	target := filepath.Join(managedRoot, "clone")
	result, err := CloneWorkspace(context.Background(), CloneWorkspaceOptions{WorkspaceID: "workspace-clone", ProjectID: "project-clone", Path: target, RemoteURL: server.URL + "/project.git", Branch: "main", ExpectedHead: head}, []string{managedRoot})
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, result.Path, target)
	if result.Branch != "main" || result.CommitSHA != head {
		t.Fatalf("unexpected clone result: %#v", result)
	}
	if marker := strings.TrimSpace(testGitOutput(t, target, "config", "--local", "--get", "wio.projectId")); marker != "project-clone" {
		t.Fatalf("project marker missing: %q", marker)
	}
	wrongTarget := filepath.Join(managedRoot, "wrong-head")
	if _, err := CloneWorkspace(context.Background(), CloneWorkspaceOptions{WorkspaceID: "workspace-wrong", ProjectID: "project-clone", Path: wrongTarget, RemoteURL: server.URL + "/project.git", Branch: "main", ExpectedHead: strings.Repeat("0", len(head))}, []string{managedRoot}); err == nil || !strings.Contains(err.Error(), "expected branch and commit") {
		t.Fatalf("mismatched remote head was not rejected: %v", err)
	}
	if _, err := os.Stat(wrongTarget); !os.IsNotExist(err) {
		t.Fatalf("failed clone left target behind: %v", err)
	}
}

func TestWorkspaceLifecycleMoveCopyAndDelete(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	source := createRepository(t, root, "source")
	moved := filepath.Join(root, "moved")
	resolved, err := MoveWorkspace(context.Background(), source, moved, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, resolved, moved)
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move: %v", err)
	}

	copied := filepath.Join(root, "copied")
	resolved, err = CopyWorkspace(context.Background(), moved, copied, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, resolved, copied)
	if _, err := GetStatus(context.Background(), copied, []string{root}); err != nil {
		t.Fatalf("copied repository is invalid: %v", err)
	}
	writeFile(t, filepath.Join(copied, "dirty.txt"), "dirty")
	if err := DeleteWorkspace(context.Background(), copied, false, []string{root}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty delete was not rejected: %v", err)
	}
	if err := DeleteWorkspace(context.Background(), copied, true, []string{root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(copied); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after delete: %v", err)
	}
}

func TestMoveLinkedWorktreeUsesGitMetadata(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	main := createRepository(t, root, "main")
	linked := filepath.Join(root, "linked")
	testGit(t, main, "worktree", "add", "-b", "feature/lifecycle", linked)
	if _, err := MoveWorkspace(context.Background(), main, filepath.Join(root, "main-moved"), []string{root}); err == nil || !strings.Contains(err.Error(), "linked worktrees") {
		t.Fatalf("moving a primary workspace with linked worktrees was not rejected: %v", err)
	}
	target := filepath.Join(root, "linked-moved")
	resolved, err := MoveWorkspace(context.Background(), linked, target, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	assertSamePath(t, resolved, target)
	output := testGitOutput(t, target, "rev-parse", "--show-toplevel")
	assertSamePath(t, strings.TrimSpace(output), target)
	if err := DeleteWorkspace(context.Background(), target, false, []string{root}); err != nil {
		t.Fatalf("delete moved linked worktree: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("linked worktree still exists after delete: %v", err)
	}
}

func TestCopyRejectsSymbolicLinksAndManagedRootEscapes(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	source := createRepository(t, root, "source")
	outside := t.TempDir()
	link := filepath.Join(source, "outside-link")
	if err := os.Symlink(filepath.Join(outside, "secret"), link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires additional Windows privileges: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := CopyWorkspace(context.Background(), source, filepath.Join(root, "copy"), []string{root}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symbolic link was not rejected: %v", err)
	}
	if _, err := MoveWorkspace(context.Background(), source, filepath.Join(outside, "moved"), []string{root}); err == nil || !strings.Contains(err.Error(), "outside managed roots") {
		t.Fatalf("managed-root escape was not rejected: %v", err)
	}
}

func testGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	output, err := runGit(context.Background(), directory, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func assertSamePath(t *testing.T, first, second string) {
	t.Helper()
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	if firstErr != nil || secondErr != nil || !os.SameFile(firstInfo, secondInfo) {
		t.Fatalf("paths do not identify the same entry: %q %q (%v, %v)", first, second, firstErr, secondErr)
	}
}
