package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/protocol"
)

var skippedDirectories = map[string]bool{
	".cache": true, ".local": true, ".npm": true, ".pnpm-store": true,
	"node_modules": true, "vendor": true, "proc": true, "sys": true,
}

const importStagingPrefix = ".wio-import-"

var skippedFileTreeDirectories = map[string]bool{
	".git": true, ".cache": true, ".next": true, ".nuxt": true, ".output": true,
	".pnpm-store": true, "build": true, "coverage": true, "dist": true,
	"node_modules": true, "target": true, "vendor": true,
}

func Discover(ctx context.Context, roots []string, limit int) (protocol.Inventory, error) {
	if limit <= 0 {
		limit = 200
	}
	repositories := make([]protocol.Repository, 0)
	seen := make(map[string]bool)
	for _, root := range roots {
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) {
			continue
		}
		err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !entry.IsDir() {
				return nil
			}
			if current != root {
				if skippedDirectories[entry.Name()] || strings.HasPrefix(entry.Name(), importStagingPrefix) {
					return filepath.SkipDir
				}
			}
			gitPath := filepath.Join(current, ".git")
			if _, err := os.Stat(gitPath); err != nil {
				return nil
			}
			canonical, err := filepath.EvalSymlinks(current)
			if err != nil {
				canonical = current
			}
			if seen[canonical] {
				return filepath.SkipDir
			}
			seen[canonical] = true
			repository := inspect(ctx, canonical)
			repositories = append(repositories, repository)
			if len(repositories) >= limit {
				return errLimitReached
			}
			return filepath.SkipDir
		})
		if err != nil && !errors.Is(err, errLimitReached) && !errors.Is(err, fs.ErrNotExist) {
			return protocol.Inventory{}, err
		}
		if len(repositories) >= limit {
			break
		}
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Path < repositories[j].Path })
	return protocol.Inventory{Repositories: repositories}, nil
}

var errLimitReached = errors.New("repository limit reached")

func ListWorkspaceFiles(ctx context.Context, root string, allowedRoots []string) (protocol.WorkspaceFilesResult, error) {
	root, err := resolveWorkspaceRoot(root, allowedRoots)
	if err != nil {
		return protocol.WorkspaceFilesResult{}, err
	}

	result := protocol.WorkspaceFilesResult{Files: make([]protocol.WorkspaceFile, 0)}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			if current == root {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if current == root {
			return nil
		}
		if entry.IsDir() && skippedFileTreeDirectories[entry.Name()] {
			return filepath.SkipDir
		}
		relative, relativeErr := filepath.Rel(root, current)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		if len(relative) > 1024 {
			result.Truncated = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Count(relative, "/") >= 20 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		item := protocol.WorkspaceFile{Path: relative, Kind: kind}
		if kind == "file" {
			if entryInfo, infoErr := entry.Info(); infoErr == nil {
				item.Size = entryInfo.Size()
			}
		}
		result.Files = append(result.Files, item)
		return nil
	})
	if err != nil {
		return protocol.WorkspaceFilesResult{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func ReadWorkspaceFile(ctx context.Context, root, relative string, allowedRoots []string, limit int64) (protocol.WorkspaceFilePreviewResult, error) {
	if limit <= 0 {
		limit = 1024 * 1024
	}
	root, err := resolveWorkspaceRoot(root, allowedRoots)
	if err != nil {
		return protocol.WorkspaceFilePreviewResult{}, err
	}
	relative = filepath.FromSlash(strings.TrimSpace(relative))
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return protocol.WorkspaceFilePreviewResult{}, errors.New("file path must stay inside the workspace")
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return protocol.WorkspaceFilePreviewResult{}, err
	}
	if !within(root, target) {
		return protocol.WorkspaceFilePreviewResult{}, errors.New("file path resolves outside the workspace")
	}
	info, err := os.Stat(target)
	if err != nil {
		return protocol.WorkspaceFilePreviewResult{}, err
	}
	if !info.Mode().IsRegular() {
		return protocol.WorkspaceFilePreviewResult{}, errors.New("file preview only supports regular files")
	}
	if err := ctx.Err(); err != nil {
		return protocol.WorkspaceFilePreviewResult{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return protocol.WorkspaceFilePreviewResult{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return protocol.WorkspaceFilePreviewResult{}, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
		for removed := 0; removed < utf8.UTFMax && len(data) > 0 && !utf8.Valid(data); removed++ {
			data = data[:len(data)-1]
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return protocol.WorkspaceFilePreviewResult{}, errors.New("file preview only supports UTF-8 text files")
	}
	return protocol.WorkspaceFilePreviewResult{Path: filepath.ToSlash(clean), Content: string(data), Size: info.Size(), Truncated: truncated}, nil
}

// ResolveWorkspaceRoot validates a workspace against configured roots and
// returns its canonical path, including symlink checks.
func ResolveWorkspaceRoot(root string, allowedRoots []string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workspace path is not a directory")
	}
	for _, candidate := range allowedRoots {
		candidate, candidateErr := filepath.Abs(filepath.Clean(candidate))
		if candidateErr != nil {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
			candidate = resolved
		}
		if within(candidate, root) {
			return root, nil
		}
	}
	return "", errors.New("workspace path is outside the configured scan roots")
}

func resolveWorkspaceRoot(root string, allowedRoots []string) (string, error) {
	return ResolveWorkspaceRoot(root, allowedRoots)
}

// repositoryOwned decides whether discovery may run Git in a repository.
// Tests replace it to simulate repositories owned by another user.
var repositoryOwned = gitrepository.RepositoryOwnedByCurrentUser

// inspect reads repository metadata. Repositories owned by another user are
// reported as present without running Git in them: their configuration is
// controlled by that user, and Git refuses them anyway unless an administrator
// explicitly trusts them.
func inspect(ctx context.Context, repositoryPath string) protocol.Repository {
	repository := protocol.Repository{
		Path:       repositoryPath,
		Name:       filepath.Base(repositoryPath),
		Discovered: time.Now().UTC().Format(time.RFC3339),
	}
	if !repositoryOwned(repositoryPath) {
		return repository
	}
	repository.RemoteURL = gitrepository.RedactRemoteURL(gitOutput(ctx, repositoryPath, "remote", "get-url", "origin"))
	repository.Branch = gitOutput(ctx, repositoryPath, "branch", "--show-current")
	repository.CommitSHA = gitOutput(ctx, repositoryPath, "rev-parse", "HEAD")
	// Submodule worktrees are not inspected: that would run Git with each
	// submodule's own configuration.
	repository.Dirty = gitOutput(ctx, repositoryPath, "status", "--porcelain", "--ignore-submodules=dirty") != ""
	return repository
}

// gitOutput runs a read-only Git command with repository-configured programs
// (fsmonitor, hooks, filter drivers, ...) disabled.
func gitOutput(ctx context.Context, repositoryPath string, args ...string) string {
	output, err := gitrepository.InspectOutput(ctx, repositoryPath, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func Import(ctx context.Context, cloneRoot string, command protocol.GitImportCommand) (string, error) {
	cloneRoot = filepath.Clean(cloneRoot)
	if !filepath.IsAbs(cloneRoot) {
		return "", errors.New("clone root must be absolute")
	}
	destination := strings.TrimSpace(command.Destination)
	if destination == "" {
		destination = filepath.Join(cloneRoot, safeName(command.Name))
	}
	destination = filepath.Clean(destination)
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(cloneRoot, destination)
	}
	if destination == cloneRoot || !within(cloneRoot, destination) {
		return "", errors.New("destination must stay within the configured clone root")
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := importParent(cloneRoot, filepath.Dir(destination))
	if err != nil {
		return "", err
	}
	// From here on only the resolved parent is used, so the clone cannot be
	// redirected by symbolic links in the requested path.
	final := filepath.Join(parent, filepath.Base(destination))
	staging, err := os.MkdirTemp(parent, importStagingPrefix)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o750); err != nil {
		return "", err
	}
	process := childprocess.CommandContext(ctx, "git", "clone", "--", command.RemoteURL, staging)
	output, err := childprocess.CombinedOutput(process)
	if err != nil {
		return "", fmt.Errorf("git clone: %w: %s", err, truncate(string(output), 4096))
	}
	if _, err := os.Lstat(final); err == nil {
		return "", fmt.Errorf("destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(staging, final); err != nil {
		return "", fmt.Errorf("publish cloned repository: %w", err)
	}
	return destination, nil
}

// importParent creates the parent directory of an import destination and
// returns its resolved path. The lexical check in Import cannot see symbolic
// links: a link inside the clone root could otherwise send the directories
// and the clone anywhere the agent can write. Existing components are
// resolved before anything is created and everything is checked again once
// the directory exists.
func importParent(cloneRoot, parent string) (string, error) {
	escape := errors.New("destination resolves outside the configured clone root")
	root, err := resolveExistingPrefix(cloneRoot)
	if err != nil {
		return "", err
	}
	resolved, err := resolveExistingPrefix(parent)
	if err != nil {
		return "", err
	}
	if !within(root, resolved) {
		return "", escape
	}
	// MkdirAll never follows a symbolic link it would have to create, so a
	// dangling link in a missing component makes it fail instead.
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return "", err
	}
	if root, err = filepath.EvalSymlinks(cloneRoot); err != nil {
		return "", err
	}
	if resolved, err = filepath.EvalSymlinks(parent); err != nil {
		return "", err
	}
	if !within(root, resolved) {
		return "", escape
	}
	return resolved, nil
}

// resolveExistingPrefix resolves symbolic links in the longest existing
// prefix of path and appends the components that do not exist yet.
func resolveExistingPrefix(path string) (string, error) {
	var missing []string
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(current)
		if next == current {
			return "", err
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = next
	}
}

func within(root, child string) bool {
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	name := strings.Trim(builder.String(), "-")
	if name == "" {
		return "repository"
	}
	return name
}

func truncate(value string, size int) string {
	if len(value) <= size {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(value[:size]) + "..."
}
