package gitrepository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const (
	maxGitChangePaths = 512
	maxCommitMessage  = 20 * 1024
)

func CreateBranch(ctx context.Context, repositoryPath, name, startPoint string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateBranch(ctx, name); err != nil {
		return err
	}
	args := []string{"branch", name}
	if strings.TrimSpace(startPoint) != "" {
		if err := validateRef(startPoint); err != nil {
			return err
		}
		args = append(args, startPoint)
	}
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("create branch: %s", cleanGitError(output, err))
	}
	return nil
}

func RenameBranch(ctx context.Context, repositoryPath, name, newName string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateBranch(ctx, name); err != nil {
		return err
	}
	if err := validateBranch(ctx, newName); err != nil {
		return err
	}
	output, err := runGit(ctx, repositoryPath, "branch", "-m", name, newName)
	if err != nil {
		return fmt.Errorf("rename branch: %s", cleanGitError(output, err))
	}
	return nil
}

func DeleteBranch(ctx context.Context, repositoryPath, name string, force bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateBranch(ctx, name); err != nil {
		return err
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	output, err := runGit(ctx, repositoryPath, "branch", flag, name)
	if err != nil {
		return fmt.Errorf("delete branch: %s", cleanGitError(output, err))
	}
	return nil
}

func Checkout(ctx context.Context, repositoryPath, ref string, detach bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	args := []string{"switch"}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, ref)
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("switch branch: %s", cleanGitError(output, err))
	}
	return nil
}

func AddRemote(ctx context.Context, repositoryPath, name, remoteURL string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateRemoteName(name); err != nil {
		return err
	}
	if err := validateRemoteURL(strings.TrimSpace(remoteURL)); err != nil {
		return err
	}
	output, err := runGit(ctx, repositoryPath, "remote", "add", name, remoteURL)
	if err != nil {
		return fmt.Errorf("add remote: %s", cleanGitError(output, err))
	}
	return nil
}

func SetRemoteURL(ctx context.Context, repositoryPath, name, remoteURL string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateRemoteName(name); err != nil {
		return err
	}
	if err := validateRemoteURL(strings.TrimSpace(remoteURL)); err != nil {
		return err
	}
	output, err := runGit(ctx, repositoryPath, "remote", "set-url", name, remoteURL)
	if err != nil {
		return fmt.Errorf("update remote: %s", cleanGitError(output, err))
	}
	return nil
}

func RemoveRemote(ctx context.Context, repositoryPath, name string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateRemoteName(name); err != nil {
		return err
	}
	output, err := runGit(ctx, repositoryPath, "remote", "remove", name)
	if err != nil {
		return fmt.Errorf("remove remote: %s", cleanGitError(output, err))
	}
	return nil
}

func Fetch(ctx context.Context, repositoryPath, remote string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	args := []string{"fetch", "--prune"}
	if strings.TrimSpace(remote) != "" {
		if err := validateRemoteName(remote); err != nil {
			return err
		}
		args = append(args, remote)
	}
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("fetch: %s", cleanGitError(output, err))
	}
	return nil
}

func Pull(ctx context.Context, repositoryPath, remote, branch string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	args := []string{"pull", "--ff-only"}
	if remote != "" {
		if err := validateRemoteName(remote); err != nil {
			return err
		}
		args = append(args, remote)
	}
	if branch != "" {
		if err := validateBranch(ctx, branch); err != nil {
			return err
		}
		args = append(args, branch)
	}
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("pull: %s", cleanGitError(output, err))
	}
	return nil
}

func Push(ctx context.Context, repositoryPath, remote, ref string, setUpstream bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	if err := validateRemoteName(remote); err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	args := []string{"push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	args = append(args, remote, ref)
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("push: %s", cleanGitError(output, err))
	}
	return nil
}

// Stage stages the selected workspace changes. When all is true, the complete
// worktree is staged, including deletions.
func Stage(ctx context.Context, repositoryPath string, paths []string, all bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	paths, err = selectWorkspaceChangePaths(ctx, repositoryPath, paths, all, managedRoots, func(change WorkspaceChange) bool {
		return change.Unstaged || change.Status == "untracked"
	})
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	args := []string{"add", "--all", "--"}
	args = append(args, paths...)
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("stage changes: %s", cleanGitError(output, err))
	}
	return nil
}

// Unstage removes the selected changes from the index while preserving the
// working tree. It also works before the repository has its first commit.
func Unstage(ctx context.Context, repositoryPath string, paths []string, all bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	paths, err = selectWorkspaceChangePaths(ctx, repositoryPath, paths, all, managedRoots, func(change WorkspaceChange) bool {
		return change.Staged
	})
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	if _, headErr := runGit(ctx, repositoryPath, "rev-parse", "--verify", "HEAD"); headErr != nil {
		args := []string{"rm", "--cached", "-r", "--ignore-unmatch", "--"}
		args = append(args, paths...)
		output, rmErr := runGit(ctx, repositoryPath, args...)
		if rmErr != nil {
			return fmt.Errorf("unstage changes: %s", cleanGitError(output, rmErr))
		}
		return nil
	}
	args := []string{"restore", "--staged", "--"}
	args = append(args, paths...)
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return fmt.Errorf("unstage changes: %s", cleanGitError(output, err))
	}
	return nil
}

// DiscardUnstaged restores tracked files to their index version and removes
// untracked files. It deliberately only accepts changes that have an unstaged
// portion, so staged work is not silently destroyed.
func DiscardUnstaged(ctx context.Context, repositoryPath string, paths []string, all bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	paths, err = selectWorkspaceChangePaths(ctx, repositoryPath, paths, all, managedRoots, func(change WorkspaceChange) bool {
		return change.Status != "conflicted" && (change.Unstaged || change.Status == "untracked")
	})
	if err != nil {
		return err
	}
	changes, err := ListWorkspaceChanges(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	byPath := make(map[string]WorkspaceChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	for _, path := range paths {
		var output []byte
		if byPath[path].Status == "untracked" {
			output, err = runGit(ctx, repositoryPath, "clean", "-f", "--", path)
		} else {
			output, err = runGit(ctx, repositoryPath, "restore", "--worktree", "--", path)
		}
		if err != nil {
			return fmt.Errorf("discard changes: %s", cleanGitError(output, err))
		}
	}
	return nil
}

// DiscardChanges restores the selected files to the last commit, including
// changes that are currently staged. Callers must opt into this explicitly
// because it removes both index and working-tree changes.
func DiscardChanges(ctx context.Context, repositoryPath string, paths []string, all bool, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	selected, err := selectWorkspaceChangePaths(ctx, repositoryPath, paths, all, managedRoots, func(change WorkspaceChange) bool {
		return change.Status != "conflicted" && (change.Staged || change.Unstaged || change.Status == "untracked")
	})
	if err != nil {
		return err
	}
	changes, err := ListWorkspaceChanges(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	selectedSet := make(map[string]struct{}, len(selected))
	for _, path := range selected {
		selectedSet[path] = struct{}{}
	}
	staged := make([]string, 0, len(selected))
	for _, change := range changes {
		if _, ok := selectedSet[change.Path]; ok && change.Staged {
			staged = append(staged, change.Path)
		}
	}
	if len(staged) > 0 {
		if err := Unstage(ctx, repositoryPath, staged, false, managedRoots); err != nil {
			return err
		}
	}
	return DiscardUnstaged(ctx, repositoryPath, selected, false, managedRoots)
}

func CommitChanges(ctx context.Context, repositoryPath, message string, managedRoots []string) error {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("commit message is required")
	}
	if len(message) > maxCommitMessage {
		return fmt.Errorf("commit message must be at most %d bytes", maxCommitMessage)
	}
	if strings.ContainsRune(message, '\x00') {
		return errors.New("commit message contains a NUL character")
	}
	output, err := runGit(ctx, repositoryPath, "commit", "--no-gpg-sign", "-m", message)
	if err != nil {
		return fmt.Errorf("commit changes: %s", cleanGitError(output, err))
	}
	return nil
}

func selectWorkspaceChangePaths(ctx context.Context, repositoryPath string, paths []string, all bool, managedRoots []string, predicate func(WorkspaceChange) bool) ([]string, error) {
	changes, err := ListWorkspaceChanges(ctx, repositoryPath, managedRoots)
	if err != nil {
		return nil, err
	}
	if all {
		selected := make([]string, 0, len(changes))
		for _, change := range changes {
			if predicate(change) {
				selected = append(selected, change.Path)
			}
		}
		return selected, nil
	}
	if len(paths) == 0 {
		return nil, errors.New("at least one changed path is required")
	}
	if len(paths) > maxGitChangePaths {
		return nil, fmt.Errorf("too many changed paths; maximum is %d", maxGitChangePaths)
	}
	byPath := make(map[string]WorkspaceChange, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	selected := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path, err := normalizeChangePath(rawPath)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[path]; ok {
			continue
		}
		change, ok := byPath[path]
		if !ok || !predicate(change) {
			return nil, fmt.Errorf("path is not an eligible workspace change: %s", path)
		}
		seen[path] = struct{}{}
		selected = append(selected, path)
	}
	return selected, nil
}

func validateBranch(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("branch name is required")
	}
	output, err := runGit(ctx, "", "check-ref-format", "--branch", name)
	if err != nil {
		return fmt.Errorf("invalid branch name: %s", cleanGitError(output, err))
	}
	return nil
}

func validateRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") || containsWhitespaceOrControl(ref) {
		return errors.New("invalid Git reference")
	}
	return nil
}

func validateRemoteName(name string) error {
	if !remoteNamePattern.MatchString(strings.TrimSpace(name)) {
		return errors.New("invalid remote name")
	}
	return nil
}
