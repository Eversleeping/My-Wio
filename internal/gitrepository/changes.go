package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxWorkspaceDiffBytes int64 = 1024 * 1024

type WorkspaceChange struct {
	Path     string `json:"path"`
	OldPath  string `json:"old_path,omitempty"`
	Status   string `json:"status"`
	Staged   bool   `json:"staged"`
	Unstaged bool   `json:"unstaged"`
}

type WorkspaceDiff struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

func ListWorkspaceChanges(ctx context.Context, repositoryPath string, roots []string) ([]WorkspaceChange, error) {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, roots)
	if err != nil {
		return nil, err
	}
	output, err := runGit(ctx, repositoryPath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("list workspace changes: %s", cleanGitError(output, err))
	}
	entries := bytes.Split(output, []byte{0})
	changes := make([]WorkspaceChange, 0, len(entries))
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if len(entry) == 0 {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, errors.New("parse workspace changes: invalid porcelain entry")
		}
		x, y := entry[0], entry[1]
		changePath, pathErr := normalizeChangePath(string(entry[3:]))
		if pathErr != nil {
			return nil, pathErr
		}
		change := WorkspaceChange{
			Path:     changePath,
			Status:   workspaceChangeStatus(x, y),
			Staged:   x != ' ' && x != '?' && x != '!',
			Unstaged: y != ' ' && y != '?' && y != '!',
		}
		if x == '?' && y == '?' {
			change.Unstaged = true
		}
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			index++
			if index >= len(entries) || len(entries[index]) == 0 {
				return nil, errors.New("parse workspace changes: renamed path is missing")
			}
			change.OldPath, pathErr = normalizeChangePath(string(entries[index]))
			if pathErr != nil {
				return nil, pathErr
			}
		}
		if change.Status != "ignored" {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func ReadWorkspaceDiff(ctx context.Context, repositoryPath, relativePath, oldPath string, roots []string, limit int64) (WorkspaceDiff, error) {
	if limit <= 0 || limit > MaxWorkspaceDiffBytes {
		limit = MaxWorkspaceDiffBytes
	}
	repositoryPath, err := resolveRepository(ctx, repositoryPath, roots)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	relativePath, err = normalizeChangePath(relativePath)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	if oldPath != "" {
		oldPath, err = normalizeChangePath(oldPath)
		if err != nil {
			return WorkspaceDiff{}, err
		}
	}
	changes, err := ListWorkspaceChanges(ctx, repositoryPath, roots)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	var selected *WorkspaceChange
	for index := range changes {
		if changes[index].Path == relativePath {
			selected = &changes[index]
			break
		}
	}
	if selected == nil {
		return WorkspaceDiff{}, errors.New("file is not modified")
	}
	if oldPath == "" {
		oldPath = selected.OldPath
	} else if selected.OldPath != oldPath {
		return WorkspaceDiff{}, errors.New("modified file path no longer matches the snapshot")
	}
	if selected.Status == "untracked" {
		return newFileDiff(repositoryPath, relativePath, limit)
	}
	if _, headErr := runGit(ctx, repositoryPath, "rev-parse", "--verify", "HEAD"); headErr != nil && selected.Status == "added" {
		return newFileDiff(repositoryPath, relativePath, limit)
	}
	args := []string{"--literal-pathspecs", "diff", "--no-ext-diff", "--no-color", "--find-renames", "--unified=3", "HEAD", "--"}
	if oldPath != "" {
		args = append(args, filepath.FromSlash(oldPath))
	}
	args = append(args, filepath.FromSlash(relativePath))
	content, truncated, err := runGitOutputLimited(ctx, repositoryPath, limit, args...)
	if err != nil {
		return WorkspaceDiff{}, fmt.Errorf("read workspace diff: %w", err)
	}
	diff := WorkspaceDiff{Path: relativePath, Content: string(content), Truncated: truncated}
	diff.Binary = bytes.Contains(content, []byte("Binary files ")) || bytes.Contains(content, []byte("GIT binary patch"))
	diff.Additions, diff.Deletions = workspaceDiffStats(diff.Content)
	return diff, nil
}

func workspaceChangeStatus(x, y byte) string {
	switch {
	case x == '?' && y == '?':
		return "untracked"
	case x == '!' && y == '!':
		return "ignored"
	case x == 'U' || y == 'U' || x == 'A' && y == 'A' || x == 'D' && y == 'D':
		return "conflicted"
	case x == 'R' || y == 'R':
		return "renamed"
	case x == 'C' || y == 'C':
		return "copied"
	case x == 'D' || y == 'D':
		return "deleted"
	case x == 'A' || y == 'A':
		return "added"
	default:
		return "modified"
	}
}

func normalizeChangePath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("modified file path is invalid")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("modified file path must stay inside the workspace")
	}
	return filepath.ToSlash(clean), nil
}

func newFileDiff(repositoryPath, relativePath string, limit int64) (WorkspaceDiff, error) {
	filename := filepath.Join(repositoryPath, filepath.FromSlash(relativePath))
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return WorkspaceDiff{}, fmt.Errorf("resolve modified file: %w", err)
	}
	relative, err := filepath.Rel(repositoryPath, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return WorkspaceDiff{}, errors.New("modified file resolves outside the workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	if !info.Mode().IsRegular() {
		return WorkspaceDiff{}, errors.New("diff preview only supports regular files")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return WorkspaceDiff{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return WorkspaceDiff{}, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return WorkspaceDiff{Path: relativePath, Content: "Binary files /dev/null and b/" + relativePath + " differ\n", Binary: true, Truncated: truncated}, nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", relativePath, relativePath, relativePath, len(lines))
	for _, line := range lines {
		builder.WriteByte('+')
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	content := builder.String()
	if int64(len(content)) > limit {
		content = strings.ToValidUTF8(content[:limit], "")
		truncated = true
	}
	return WorkspaceDiff{Path: relativePath, Content: content, Additions: len(lines), Truncated: truncated}, nil
}

func runGitOutputLimited(ctx context.Context, directory string, limit int64, args ...string) ([]byte, bool, error) {
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, false, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	truncated := int64(len(output)) > limit
	if truncated {
		output = output[:limit]
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, false, readErr
	}
	if truncated {
		return output, true, nil
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, false, errors.New(message)
	}
	return output, false, nil
}

func workspaceDiffStats(content string) (additions, deletions int) {
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletions++
		}
	}
	return additions, deletions
}
