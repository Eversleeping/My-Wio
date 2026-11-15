package agent

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

func (c *Client) runDeployment(ctx context.Context, command protocol.DeployCommand) error {
	if command.SourceType == "workspace" && !pathWithinRoots(command.SourcePath, c.inventoryRoots()) {
		return errors.New("deployment workspace is outside configured inventory roots")
	}
	status := func(state, message, resolved, detectedPublicURL, content string) {
		_ = c.queue(protocol.AgentKindDeploymentStatus, protocol.DeploymentStatus{DeploymentID: command.DeploymentID, Status: state, Message: redactDeploymentCommandText(message, command), ResolvedCommit: resolved, DetectedPublicURL: detectedPublicURL, Content: redactDeploymentCommandText(content, command)}, true)
	}
	err := c.deployer.Deploy(ctx, command, status)
	if err != nil {
		status("failed", truncate(err.Error(), 8192), "", "", "")
		return errors.New(redactDeploymentCommandText(err.Error(), command))
	}
	return err
}

func redactDeploymentCommandText(value string, command protocol.DeployCommand) string {
	value = redactDeploymentText(value, command.Environment, command.Repository)
	if command.Recovery != nil {
		value = redactDeploymentText(value, command.Recovery.Environment, "")
	}
	return value
}

func pathWithinRoots(path string, roots []string) bool {
	clean := filepath.Clean(path)
	for _, root := range roots {
		relative, err := filepath.Rel(filepath.Clean(root), clean)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c *Client) runRollback(ctx context.Context, command protocol.RollbackCommand) error {
	status := func(state, message, resolved, _ string, content string) {
		_ = c.queue(protocol.AgentKindDeploymentStatus, protocol.DeploymentStatus{DeploymentID: command.DeploymentID, Status: state, Message: redactDeploymentText(message, command.Environment, ""), ResolvedCommit: resolved, Content: redactDeploymentText(content, command.Environment, "")}, true)
	}
	err := c.deployer.Rollback(ctx, command, status)
	if err != nil {
		status("failed", truncate(redactDeploymentText(err.Error(), command.Environment, ""), 8192), "", "", "")
	}
	return err
}

func (c *Client) runContainerAction(ctx context.Context, command protocol.ContainerActionCommand) (protocol.ContainerActionResult, error) {
	if command.TargetID == "" {
		return protocol.ContainerActionResult{}, errors.New("container action target is required")
	}
	result, err := c.deployer.ContainerAction(ctx, command)
	result.Message = redactDeploymentText(result.Message, command.Environment, "")
	result.Content = redactDeploymentText(result.Content, command.Environment, "")
	if err != nil {
		return result, errors.New(redactDeploymentText(err.Error(), command.Environment, ""))
	}
	return result, nil
}

func redactDeploymentText(value string, environment map[string]string, repository string) string {
	secrets := make([]string, 0, len(environment)+4)
	for _, secret := range environment {
		secrets = append(secrets, secret)
	}
	if remote, err := url.Parse(repository); err == nil {
		if remote.User != nil {
			if password, ok := remote.User.Password(); ok {
				secrets = append(secrets, password)
			}
		}
		for key, values := range remote.Query() {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
				secrets = append(secrets, values...)
			}
		}
	}
	for _, secret := range secrets {
		if len(secret) >= 4 {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
