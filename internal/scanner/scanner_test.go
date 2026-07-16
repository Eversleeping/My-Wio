package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "service")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.email", "test@example.com")
	runGit(t, repository, "config", "user.name", "Wio Test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "initial")
	runGit(t, repository, "remote", "add", "origin", "https://example.com/team/service.git")
	inventory, err := Discover(context.Background(), []string{root}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 {
		t.Fatalf("expected one repository, got %d", len(inventory.Repositories))
	}
	item := inventory.Repositories[0]
	if item.Name != "service" || item.RemoteURL != "https://example.com/team/service.git" || item.CommitSHA == "" || item.Dirty {
		t.Fatalf("unexpected repository: %#v", item)
	}
}

func TestWithinRejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if !within(root, filepath.Join(root, "project")) {
		t.Fatal("child path was rejected")
	}
	if within(root, filepath.Join(root, "..", "outside")) {
		t.Fatal("traversal path was accepted")
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
