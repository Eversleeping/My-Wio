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

func TestListWorkspaceFilesFiltersGeneratedDirectoriesAndEnforcesRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	for _, directory := range []string{filepath.Join(root, "src"), filepath.Join(root, "node_modules", "package"), filepath.Join(root, ".git")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(root, "README.md"):                       "readme",
		filepath.Join(root, "src", "main.ts"):                  "export {}",
		filepath.Join(root, "node_modules", "package", "x.js"): "ignored",
		filepath.Join(root, ".git", "config"):                  "ignored",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := ListWorkspaceFiles(context.Background(), root, []string{base}, 100)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{}
	for _, entry := range result.Files {
		entries[entry.Path] = entry.Kind
	}
	if entries["README.md"] != "file" || entries["src"] != "directory" || entries["src/main.ts"] != "file" {
		t.Fatalf("expected source files were not returned: %#v", entries)
	}
	if _, exists := entries["node_modules"]; exists {
		t.Fatalf("node_modules was included: %#v", entries)
	}
	if _, exists := entries[".git"]; exists {
		t.Fatalf(".git was included: %#v", entries)
	}

	limited, err := ListWorkspaceFiles(context.Background(), root, []string{base}, 2)
	if err != nil || !limited.Truncated || len(limited.Files) != 2 {
		t.Fatalf("unexpected limited result: %#v %v", limited, err)
	}
	if _, err := ListWorkspaceFiles(context.Background(), root, []string{filepath.Join(base, "other")}, 100); err == nil {
		t.Fatal("workspace outside the configured roots was accepted")
	}
}

func TestReadWorkspaceFileEnforcesWorkspaceAndTextLimits(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.ts"), []byte("export const answer = 42;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ReadWorkspaceFile(context.Background(), root, "src/main.ts", []string{base}, 12)
	if err != nil || result.Path != "src/main.ts" || result.Content != "export const" || !result.Truncated || result.Size != 26 {
		t.Fatalf("unexpected preview: %#v %v", result, err)
	}
	if _, err := ReadWorkspaceFile(context.Background(), root, "../outside.txt", []string{base}, 1024); err == nil {
		t.Fatal("workspace traversal was accepted")
	}
	if _, err := ReadWorkspaceFile(context.Background(), root, "binary.dat", []string{base}, 1024); err == nil {
		t.Fatal("binary file was accepted")
	}

	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link.txt")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := ReadWorkspaceFile(context.Background(), root, "outside-link.txt", []string{base}, 1024); err == nil {
			t.Fatal("symlink outside the workspace was accepted")
		}
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
