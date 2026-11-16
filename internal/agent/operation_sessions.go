package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

func (c *Client) runGeminiSkillsManageOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GeminiSkillManageCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		if c.agentUpdatePending() {
			err = errors.New("Agent update is in progress")
		} else {
			command.Workspace, err = scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
			if err == nil {
				var result protocol.GeminiSkillsResult
				result, err = c.gemini.ManageSkill(ctx, command)
				if err == nil {
					resultData, err = json.Marshal(result)
				}
			}
		}
	}

	return
}

func (c *Client) runClaudeSkillsListOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.ClaudeSkillsCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.ClaudeSkillsResult
		result, err = c.listClaudeSkills(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) runGeminiSkillsListOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GeminiSkillsCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var root string
		root, err = scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
		if err == nil {
			var result protocol.GeminiSkillsResult
			result, err = c.gemini.ListSkills(ctx, envelope.OperationID, root)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	}

	return
}

func (c *Client) runClaudeThreadForkOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.ClaudeForkCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		_, err = scanner.ResolveWorkspaceRoot(command.SourceWorkspace, c.inventoryRoots())
		if err == nil {
			var fork protocol.ClaudeForkResult
			fork, err = claudeadapter.Snapshot(ctx, c.config.StateDir, command)
			if err == nil {
				resultData, err = json.Marshal(fork)
			}
		}
	}

	return
}

func (c *Client) runGeminiThreadForkOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GeminiForkCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var root string
		root, err = scanner.ResolveWorkspaceRoot(command.SourceWorkspace, c.inventoryRoots())
		if err == nil && root != command.SourceWorkspace {
			err = errors.New("Gemini source path changed")
		}
		if err == nil {
			_, err = scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
		}
		if err == nil {
			var fork protocol.GeminiForkResult
			fork, err = c.gemini.Fork(ctx, envelope.OperationID, command)
			if err == nil {
				resultData, err = json.Marshal(fork)
			}
		}
	}

	return
}

func (c *Client) executeGeminiTurnInterrupt(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.GeminiInterruptCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.interruptGeminiAndWait(ctx, command.OperationID)
}

func (c *Client) executeGeminiCredentialsConfigure(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.ConfigureGeminiCredentialsCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.configureGeminiCredentials(command)
}

func (c *Client) executeClaudeCredentialsConfigure(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.ConfigureClaudeCredentialsCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.configureClaudeCredentials(command)
}
