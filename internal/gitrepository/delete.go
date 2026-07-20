package gitrepository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteResult describes a physical project deletion. Deleted is false when
// the requested project path was already absent.
type DeleteResult struct {
	Path    string
	Deleted bool
}

// DeleteManagedProject physically removes a Wio-created Git repository. The
// repository must be a strict descendant of managedRoot and carry the exact
// project marker written by Create.
//
// A missing path is treated as an idempotent success only after its cleaned
// path and existing parent have been resolved below managedRoot. This keeps a
// retry safe without allowing a missing or dangling symlink to bypass the
// boundary checks.
func DeleteManagedProject(ctx context.Context, projectID, repositoryPath, managedRoot string) (DeleteResult, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return DeleteResult{}, errors.New("project ID is required")
	}
	if containsControl(projectID) {
		return DeleteResult{}, errors.New("project ID contains control characters")
	}
	root, err := resolveManagedRoot(managedRoot)
	if err != nil {
		return DeleteResult{}, err
	}
	path, err := cleanAbsolutePath(repositoryPath)
	if err != nil {
		return DeleteResult{}, err
	}

	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		// The deleted directory itself is gone, so resolve its parent to ensure
		// the retry still names a path below the configured root.
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
		if parentErr != nil {
			return DeleteResult{}, fmt.Errorf("resolve project parent: %w", parentErr)
		}
		candidate := filepath.Join(parent, filepath.Base(path))
		if err := requireStrictDescendant(root, candidate); err != nil {
			return DeleteResult{}, err
		}
		return DeleteResult{Path: candidate, Deleted: false}, nil
	}
	if statErr != nil {
		return DeleteResult{}, fmt.Errorf("inspect project path: %w", statErr)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return DeleteResult{}, fmt.Errorf("resolve project path: %w", err)
	}
	if err := requireStrictDescendant(root, resolved); err != nil {
		return DeleteResult{}, err
	}
	if !info.IsDir() {
		return DeleteResult{}, errors.New("project path must be a directory")
	}

	// resolveRepository verifies that this is the Git worktree root, rather
	// than a nested directory inside a repository. It also repeats the managed
	// root check after symlink resolution.
	resolved, err = resolveRepository(ctx, resolved, []string{root})
	if err != nil {
		return DeleteResult{}, err
	}
	marker, markerErr := runGit(ctx, resolved, "config", "--local", "--get", "wio.projectId")
	if markerErr != nil {
		return DeleteResult{}, errors.New("project repository is missing the Wio project marker")
	}
	if strings.TrimSpace(string(marker)) != projectID {
		return DeleteResult{}, errors.New("project repository marker does not match project ID")
	}
	if err := ctx.Err(); err != nil {
		return DeleteResult{}, err
	}
	if err := os.RemoveAll(resolved); err != nil {
		return DeleteResult{}, fmt.Errorf("delete project directory: %w", err)
	}
	return DeleteResult{Path: resolved, Deleted: true}, nil
}

func resolveManagedRoot(root string) (string, error) {
	root, err := cleanAbsolutePath(root)
	if err != nil {
		return "", fmt.Errorf("invalid clone root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve clone root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect clone root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("clone root must be a directory")
	}
	return resolved, nil
}

func cleanAbsolutePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func requireStrictDescendant(root, path string) error {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	path, err = filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("project path must be a strict descendant of the configured clone root")
	}
	return nil
}
