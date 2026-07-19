package gitworktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

func Create(ctx context.Context, command protocol.GitWorktreeCreateCommand, roots []string) (protocol.GitWorktreeCreateResult, error) {
	source, err := allowedPath(command.SourcePath, roots, true)
	if err != nil {
		return protocol.GitWorktreeCreateResult{}, fmt.Errorf("invalid source workspace: %w", err)
	}
	target, err := allowedPath(command.TargetPath, roots, false)
	if err != nil {
		return protocol.GitWorktreeCreateResult{}, fmt.Errorf("invalid worktree path: %w", err)
	}
	if source == target {
		return protocol.GitWorktreeCreateResult{}, errors.New("worktree path must differ from source workspace")
	}
	if containsPath(source, target) || containsPath(target, source) {
		return protocol.GitWorktreeCreateResult{}, errors.New("worktree and source paths cannot contain one another")
	}
	branch := strings.TrimSpace(command.Branch)
	if branch == "" {
		return protocol.GitWorktreeCreateResult{}, errors.New("branch is required")
	}
	if output, err := git(ctx, source, "check-ref-format", "--branch", branch); err != nil {
		return protocol.GitWorktreeCreateResult{}, fmt.Errorf("invalid branch name: %s", cleanGitError(output, err))
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return protocol.GitWorktreeCreateResult{}, errors.New("worktree path already exists")
		}
		return protocol.GitWorktreeCreateResult{}, err
	}
	list, err := git(ctx, source, "worktree", "list", "--porcelain")
	if err != nil {
		return protocol.GitWorktreeCreateResult{}, fmt.Errorf("list worktrees: %s", cleanGitError(list, err))
	}
	if bytes.Contains(list, []byte("branch refs/heads/"+branch+"\n")) {
		return protocol.GitWorktreeCreateResult{}, errors.New("branch is already checked out in another worktree")
	}
	base := strings.TrimSpace(command.BaseRef)
	if base == "" {
		base = "HEAD"
	}
	output, err := git(ctx, source, "worktree", "add", "-b", branch, "--", target, base)
	if err != nil {
		return protocol.GitWorktreeCreateResult{}, fmt.Errorf("create worktree: %s", cleanGitError(output, err))
	}
	sha, err := git(ctx, target, "rev-parse", "HEAD")
	if err != nil {
		_ = Remove(context.Background(), source, target, branch, roots)
		return protocol.GitWorktreeCreateResult{}, fmt.Errorf("resolve worktree commit: %s", cleanGitError(sha, err))
	}
	return protocol.GitWorktreeCreateResult{Path: target, Branch: branch, CommitSHA: strings.TrimSpace(string(sha))}, nil
}

func Remove(ctx context.Context, source, target, branch string, roots []string) error {
	source, err := allowedPath(source, roots, true)
	if err != nil {
		return err
	}
	target, err = allowedPath(target, roots, false)
	if err != nil {
		return err
	}
	output, err := git(ctx, source, "worktree", "remove", "--force", "--", target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove worktree: %s", cleanGitError(output, err))
	}
	if branch != "" {
		output, err = git(ctx, source, "branch", "-D", "--", branch)
		if err != nil {
			return fmt.Errorf("remove worktree branch: %s", cleanGitError(output, err))
		}
	}
	return nil
}

func containsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func allowedPath(path string, roots []string, mustExist bool) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	path = filepath.Clean(path)
	if mustExist {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		path = resolved
	} else {
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", errors.New("worktree parent directory must exist")
		}
		path = filepath.Join(parent, filepath.Base(path))
	}
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if !filepath.IsAbs(root) {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return path, nil
		}
	}
	return "", errors.New("path is outside configured roots")
}

func git(ctx context.Context, directory string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", directory}, args...)
	return exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
}

func cleanGitError(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return message
}
