package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/protocol"
)

func (c *Client) createProject(ctx context.Context, command protocol.GitProjectCreateCommand) (protocol.GitProjectCreateResult, error) {
	destination, err := projectCreateDestination(c.config.CloneRoot, command.Name, command.Destination)
	if err != nil {
		return protocol.GitProjectCreateResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return protocol.GitProjectCreateResult{}, fmt.Errorf("prepare repository parent directory: %w", err)
	}
	result, err := gitrepository.Create(ctx, gitrepository.CreateOptions{
		ProjectID:          command.ProjectID,
		Path:               destination,
		InitialBranch:      command.InitialBranch,
		RemoteURL:          command.RemoteURL,
		RequireEmptyRemote: command.RequireEmptyRemote,
		InitializeREADME:   command.InitializeREADME,
	}, []string{filepath.Clean(c.config.CloneRoot)})
	if err != nil {
		return protocol.GitProjectCreateResult{}, err
	}
	return protocol.GitProjectCreateResult{
		Path:      result.Path,
		Branch:    result.Branch,
		CommitSHA: result.Head,
		Unborn:    result.Unborn,
		RemoteURL: result.RemoteURL,
	}, nil
}

func (c *Client) deleteProject(ctx context.Context, command protocol.GitProjectDeleteCommand) (protocol.GitProjectDeleteResult, error) {
	projectID := strings.TrimSpace(command.ProjectID)
	if projectID == "" {
		return protocol.GitProjectDeleteResult{}, errors.New("project ID is required")
	}
	if strings.TrimSpace(command.Path) == "" {
		return protocol.GitProjectDeleteResult{}, errors.New("project path is required")
	}
	result, err := gitrepository.DeleteManagedProject(ctx, projectID, command.Path, c.config.CloneRoot)
	if err != nil {
		return protocol.GitProjectDeleteResult{}, err
	}
	return protocol.GitProjectDeleteResult{ProjectID: projectID, WorkspaceID: strings.TrimSpace(command.WorkspaceID), Path: filepath.Clean(command.Path), Removed: result.Deleted}, nil
}

func projectCreateDestination(cloneRoot, projectName, destination string) (string, error) {
	cloneRoot = strings.TrimSpace(cloneRoot)
	if cloneRoot == "" || !filepath.IsAbs(cloneRoot) {
		return "", errors.New("clone root must be absolute")
	}
	cloneRoot, err := filepath.Abs(filepath.Clean(cloneRoot))
	if err != nil {
		return "", fmt.Errorf("resolve clone root: %w", err)
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		destination = safeProjectName(projectName)
	}
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(cloneRoot, destination)
	}
	destination, err = filepath.Abs(filepath.Clean(destination))
	if err != nil {
		return "", fmt.Errorf("resolve project destination: %w", err)
	}
	relative, err := filepath.Rel(cloneRoot, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("destination must stay below the configured clone root")
	}
	return destination, nil
}

func safeProjectName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' {
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
