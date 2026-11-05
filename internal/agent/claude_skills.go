package agent

import (
	"context"
	"errors"

	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

func (c *Client) listClaudeSkills(ctx context.Context, command protocol.ClaudeSkillsCommand) (protocol.ClaudeSkillsResult, error) {
	if c.agentUpdatePending() {
		return protocol.ClaudeSkillsResult{}, errors.New("Agent update is in progress")
	}
	root, err := scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
	if err != nil {
		return protocol.ClaudeSkillsResult{}, err
	}
	if !c.claudeMu.TryRLock() {
		return protocol.ClaudeSkillsResult{}, errors.New("Claude configuration or installation is in progress")
	}
	defer c.claudeMu.RUnlock()
	environment, err := c.claudeEnvironment()
	if err != nil {
		return protocol.ClaudeSkillsResult{}, err
	}
	return claudeadapter.ListSkills(ctx, effectiveClaudePath(c.config.StateDir), root, environment...)
}
