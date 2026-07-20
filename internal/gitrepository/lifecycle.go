package gitrepository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type CloneWorkspaceOptions struct {
	WorkspaceID  string
	ProjectID    string
	Path         string
	RemoteURL    string
	Branch       string
	ExpectedHead string
}

type CloneWorkspaceResult struct {
	Path      string
	Branch    string
	CommitSHA string
}

func CloneWorkspace(ctx context.Context, options CloneWorkspaceOptions, managedRoots []string) (result CloneWorkspaceResult, err error) {
	if strings.TrimSpace(options.WorkspaceID) == "" || strings.TrimSpace(options.ProjectID) == "" || containsControl(options.WorkspaceID) || containsControl(options.ProjectID) {
		return CloneWorkspaceResult{}, errors.New("workspace and project IDs are required")
	}
	if err := validateRemoteURL(strings.TrimSpace(options.RemoteURL)); err != nil {
		return CloneWorkspaceResult{}, err
	}
	if err := validateBranch(ctx, options.Branch); err != nil {
		return CloneWorkspaceResult{}, err
	}
	if !isObjectID(strings.TrimSpace(options.ExpectedHead)) {
		return CloneWorkspaceResult{}, errors.New("expected commit is invalid")
	}
	target, err := allowedNewPath(options.Path, managedRoots)
	if err != nil {
		return CloneWorkspaceResult{}, fmt.Errorf("invalid clone path: %w", err)
	}
	output, err := runGit(ctx, "", "clone", "--branch", options.Branch, "--single-branch", "--", options.RemoteURL, target)
	if err != nil {
		return CloneWorkspaceResult{}, fmt.Errorf("clone workspace: %s", cleanGitError(output, err))
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(target)
		}
	}()
	repository, err := resolveRepository(ctx, target, managedRoots)
	if err != nil {
		return CloneWorkspaceResult{}, err
	}
	status, err := getStatus(ctx, repository)
	if err != nil {
		return CloneWorkspaceResult{}, err
	}
	if status.Dirty || status.Detached || status.Unborn || status.Branch != options.Branch || status.Head != options.ExpectedHead {
		return CloneWorkspaceResult{}, errors.New("cloned workspace does not match the expected branch and commit")
	}
	if output, commandErr := runGit(ctx, repository, "config", "--local", "wio.projectId", options.ProjectID); commandErr != nil {
		return CloneWorkspaceResult{}, fmt.Errorf("mark cloned workspace project: %s", cleanGitError(output, commandErr))
	}
	complete = true
	verified, err := filepath.Abs(filepath.Clean(options.Path))
	if err != nil {
		return CloneWorkspaceResult{}, err
	}
	return CloneWorkspaceResult{Path: verified, Branch: status.Branch, CommitSHA: status.Head}, nil
}

func MoveWorkspace(ctx context.Context, sourcePath, targetPath string, managedRoots []string) (string, error) {
	source, linked, err := lifecycleSource(ctx, sourcePath, managedRoots)
	if err != nil {
		return "", err
	}
	target, err := allowedNewPath(targetPath, managedRoots)
	if err != nil {
		return "", fmt.Errorf("invalid target path: %w", err)
	}
	if linked {
		commonOutput, commonErr := runGit(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if commonErr != nil {
			return "", fmt.Errorf("resolve worktree common directory: %s", cleanGitError(commonOutput, commonErr))
		}
		owner := filepath.Dir(strings.TrimSpace(string(commonOutput)))
		output, moveErr := runGit(ctx, owner, "worktree", "move", source, target)
		if moveErr != nil {
			return "", fmt.Errorf("move linked worktree: %s", cleanGitError(output, moveErr))
		}
	} else {
		if err := ensureNoLinkedWorktrees(ctx, source); err != nil {
			return "", err
		}
		if err := os.Rename(source, target); err != nil {
			return "", fmt.Errorf("move workspace: %w", err)
		}
	}
	_, err = resolveRepository(ctx, target, managedRoots)
	if err != nil {
		return "", fmt.Errorf("verify moved workspace: %w", err)
	}
	verified, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return "", err
	}
	return verified, nil
}

func CopyWorkspace(ctx context.Context, sourcePath, targetPath string, managedRoots []string) (target string, err error) {
	source, linked, err := lifecycleSource(ctx, sourcePath, managedRoots)
	if err != nil {
		return "", err
	}
	if linked {
		return "", errors.New("linked worktrees cannot be copied as standalone workspaces")
	}
	if err := ensureNoLinkedWorktrees(ctx, source); err != nil {
		return "", err
	}
	target, err = allowedNewPath(targetPath, managedRoots)
	if err != nil {
		return "", fmt.Errorf("invalid target path: %w", err)
	}
	if err = os.Mkdir(target, 0o750); err != nil {
		return "", fmt.Errorf("create target workspace: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(target)
		}
	}()
	if err = copyWorkspaceTree(ctx, source, target); err != nil {
		return "", err
	}
	_, err = resolveRepository(ctx, target, managedRoots)
	if err != nil {
		return "", fmt.Errorf("verify copied workspace: %w", err)
	}
	complete = true
	verified, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return "", err
	}
	return verified, nil
}

func DeleteWorkspace(ctx context.Context, workspacePath string, force bool, managedRoots []string) error {
	workspace, linked, err := lifecycleSource(ctx, workspacePath, managedRoots)
	if err != nil {
		return err
	}
	status, err := getStatus(ctx, workspace)
	if err != nil {
		return err
	}
	if status.Dirty && !force {
		return errors.New("workspace has uncommitted changes")
	}
	if linked {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, workspace)
		commonOutput, commonErr := runGit(ctx, workspace, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if commonErr != nil {
			return fmt.Errorf("resolve worktree common directory: %s", cleanGitError(commonOutput, commonErr))
		}
		owner := filepath.Dir(strings.TrimSpace(string(commonOutput)))
		output, removeErr := runGit(ctx, owner, args...)
		if removeErr != nil {
			return fmt.Errorf("remove linked worktree: %s", cleanGitError(output, removeErr))
		}
		return nil
	}
	if err := ensureNoLinkedWorktrees(ctx, workspace); err != nil {
		return err
	}
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	return nil
}

func ensureNoLinkedWorktrees(ctx context.Context, workspace string) error {
	output, err := runGit(ctx, workspace, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("inspect linked worktrees: %s", cleanGitError(output, err))
	}
	if strings.Count(string(output), "worktree ") > 1 {
		return errors.New("primary workspace has linked worktrees")
	}
	return nil
}

func lifecycleSource(ctx context.Context, raw string, managedRoots []string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !filepath.IsAbs(raw) {
		return "", false, errors.New("workspace path must be absolute")
	}
	info, err := os.Lstat(filepath.Clean(raw))
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("workspace path must not be a symbolic link")
	}
	resolved, err := resolveRepository(ctx, raw, managedRoots)
	if err != nil {
		return "", false, err
	}
	gitInfo, err := os.Lstat(filepath.Join(resolved, ".git"))
	if err != nil {
		return "", false, errors.New("workspace Git metadata is missing")
	}
	if gitInfo.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("workspace Git metadata must not be a symbolic link")
	}
	return resolved, gitInfo.Mode().IsRegular(), nil
}

func copyWorkspaceTree(ctx context.Context, source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("workspace copy escaped source path")
		}
		if relative == "." {
			return nil
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			return fmt.Errorf("workspace copy refuses symbolic link %q", relative)
		case entry.IsDir():
			return os.Mkdir(destination, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyRegularFile(path, destination, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported workspace entry %q", relative)
		}
	})
}

func copyRegularFile(source, target string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
