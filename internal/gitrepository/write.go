package gitrepository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

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
