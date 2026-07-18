package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var skippedDirectories = map[string]bool{
	".cache": true, ".local": true, ".npm": true, ".pnpm-store": true,
	"node_modules": true, "vendor": true, "proc": true, "sys": true,
}

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
			if current != root && skippedDirectories[entry.Name()] {
				return filepath.SkipDir
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

func ListWorkspaceFiles(ctx context.Context, root string, allowedRoots []string, limit int) (protocol.WorkspaceFilesResult, error) {
	if limit <= 0 {
		limit = 4000
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return protocol.WorkspaceFilesResult{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return protocol.WorkspaceFilesResult{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return protocol.WorkspaceFilesResult{}, err
	}
	if !info.IsDir() {
		return protocol.WorkspaceFilesResult{}, errors.New("workspace path is not a directory")
	}
	allowed := false
	for _, candidate := range allowedRoots {
		candidate, candidateErr := filepath.Abs(filepath.Clean(candidate))
		if candidateErr != nil {
			continue
		}
		if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
			candidate = resolved
		}
		if within(candidate, root) {
			allowed = true
			break
		}
	}
	if !allowed {
		return protocol.WorkspaceFilesResult{}, errors.New("workspace path is outside the configured scan roots")
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
		if len(result.Files) >= limit {
			result.Truncated = true
			return errLimitReached
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return protocol.WorkspaceFilesResult{}, err
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func inspect(ctx context.Context, repositoryPath string) protocol.Repository {
	return protocol.Repository{
		Path:       repositoryPath,
		Name:       filepath.Base(repositoryPath),
		RemoteURL:  gitOutput(ctx, repositoryPath, "remote", "get-url", "origin"),
		Branch:     gitOutput(ctx, repositoryPath, "branch", "--show-current"),
		CommitSHA:  gitOutput(ctx, repositoryPath, "rev-parse", "HEAD"),
		Dirty:      gitOutput(ctx, repositoryPath, "status", "--porcelain") != "",
		Discovered: time.Now().UTC().Format(time.RFC3339),
	}
}

func gitOutput(ctx context.Context, repositoryPath string, args ...string) string {
	commandArgs := append([]string{"-C", repositoryPath}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	output, err := command.Output()
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
	if !within(cloneRoot, destination) {
		return "", errors.New("destination must stay within the configured clone root")
	}
	if _, err := os.Stat(destination); err == nil {
		return "", fmt.Errorf("destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return "", err
	}
	process := exec.CommandContext(ctx, "git", "clone", "--", command.RemoteURL, destination)
	output, err := process.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git clone: %w: %s", err, truncate(string(output), 4096))
	}
	return destination, nil
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
