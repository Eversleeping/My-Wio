package gitrepository

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeleteManagedProjectIsIdempotent(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	target := filepath.Join(root, "service")
	if _, err := Create(context.Background(), CreateOptions{ProjectID: "project-1", Path: target}, []string{root}); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}

	first, err := DeleteManagedProject(context.Background(), "project-1", target, root)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Deleted || first.Path != resolvedTarget {
		t.Fatalf("unexpected first deletion result: %#v", first)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("project path still exists: %v", err)
	}

	second, err := DeleteManagedProject(context.Background(), "project-1", target, root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted || second.Path != resolvedTarget {
		t.Fatalf("unexpected retry result: %#v", second)
	}
}

func TestDeleteManagedProjectRejectsUnsafeTargets(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	root := filepath.Join(base, "managed")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("path traversal", func(t *testing.T) {
		outside := filepath.Join(base, "outside")
		if err := os.Mkdir(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		repository := filepath.Join(outside, "repository")
		if _, err := Create(context.Background(), CreateOptions{ProjectID: "outside-project", Path: repository}, []string{outside}); err != nil {
			t.Fatal(err)
		}
		traversal := filepath.Join(root, "..", "outside", "repository")
		if filepath.Clean(traversal) != repository {
			t.Fatalf("test paths are not siblings: traversal=%s repository=%s", traversal, repository)
		}
		if _, err := DeleteManagedProject(context.Background(), "outside-project", traversal, root); err == nil {
			t.Fatal("expected traversal outside clone root to be rejected")
		}
		if _, err := os.Stat(repository); err != nil {
			t.Fatalf("outside repository was changed: %v", err)
		}
	})

	t.Run("clone root itself", func(t *testing.T) {
		if _, err := DeleteManagedProject(context.Background(), "root-project", root, root); err == nil {
			t.Fatal("expected clone root deletion to be rejected")
		}
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("clone root was changed: %v", err)
		}
	})

	t.Run("not a Git repository", func(t *testing.T) {
		target := filepath.Join(root, "plain-directory")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := DeleteManagedProject(context.Background(), "plain-project", target, root); err == nil {
			t.Fatal("expected non-Git directory to be rejected")
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("non-Git directory was changed: %v", err)
		}
	})

	t.Run("marker conflict", func(t *testing.T) {
		target := filepath.Join(root, "other-project")
		if _, err := Create(context.Background(), CreateOptions{ProjectID: "actual-project", Path: target}, []string{root}); err != nil {
			t.Fatal(err)
		}
		_, err := DeleteManagedProject(context.Background(), "requested-project", target, root)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected marker conflict, got %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("conflicting repository was changed: %v", err)
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		outside := t.TempDir()
		repository := filepath.Join(outside, "linked-project")
		if _, err := Create(context.Background(), CreateOptions{ProjectID: "linked-project", Path: repository}, []string{outside}); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "linked-project")
		if err := os.Symlink(repository, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("creating symlinks requires additional Windows privileges: %v", err)
			}
			t.Fatal(err)
		}
		if _, err := DeleteManagedProject(context.Background(), "linked-project", link, root); err == nil {
			t.Fatal("expected symlink escape to be rejected")
		}
		if _, err := os.Stat(repository); err != nil {
			t.Fatalf("symlink target was changed: %v", err)
		}
	})
}
