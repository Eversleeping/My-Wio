package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
	"github.com/wio-platform/wio/internal/sessiontools"
	"time"
)

func (c *Client) runGeminiTurnInterruptOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.GeminiInterruptCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
		err = c.interruptGeminiAndWait(stopContext, command.OperationID)
		stopCancel()
		if err == nil {
			resultData, err = json.Marshal(protocol.GeminiInterruptResult{OperationID: command.OperationID, Stopped: true})
		}
	}

	return
}

func (c *Client) runClaudeTurnSteerOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.ClaudeSteerCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		err = c.claudeTurns.Steer(ctx, command)
	}

	return
}

func (c *Client) runClaudeTurnInterruptOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.ClaudeInterruptCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
		err = c.interruptClaudeAndWait(stopContext, command.OperationID)
		stopCancel()
		if err == nil {
			resultData, err = json.Marshal(protocol.GeminiInterruptResult{OperationID: command.OperationID, Stopped: true})
		}
	}

	return
}

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

func (c *Client) executeGeminiTurnStart(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if c.agentUpdatePending() {
		return errors.New("Agent update is in progress; retry the task after it reconnects")
	}
	var command protocol.GeminiTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	root, err := scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
	if err != nil {
		return err
	}
	command.Workspace = root
	environment, err := c.geminiGitEnvironment(ctx)
	if err != nil {
		return err
	}
	bridge, closeBridge, bridgeErr := c.sessionBridge(ctx, sessiontools.Ref{Engine: "gemini", ID: command.ThreadID}, envelope.OperationID)
	if bridgeErr == nil {
		command.CollaborationMCP = bridge
		defer closeBridge()
	} else {
		c.log.Warn("collaboration bridge unavailable", "engine", "gemini")
	}
	return c.gemini.RunInTrustedWorkspaceWithEnvironment(ctx, envelope.OperationID, command, environment)
}

func (c *Client) executeClaudeTurnStart(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if c.agentUpdatePending() {
		return errors.New("Agent update is in progress; retry the task after it reconnects")
	}
	var command protocol.ClaudeTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	root, err := scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
	if err != nil {
		return err
	}
	command.Workspace = root
	if !c.claudeMu.TryRLock() {
		return errors.New("Claude configuration or installation is in progress")
	}
	defer c.claudeMu.RUnlock()
	if command.Rewrite != nil {
		if err := claudeadapter.PrepareRewrite(ctx, c.config.StateDir, envelope.OperationID, &command); err != nil {
			return err
		}
		defer func() {
			_ = claudeadapter.RemoveSnapshot(c.config.StateDir, envelope.OperationID)
		}()
	}
	if command.ForkSnapshotID != "" && command.SessionID == "" {
		if command.ForkSnapshotID != command.ThreadID {
			return errors.New("Claude snapshot does not match thread")
		}
		command.ResumePath, err = claudeadapter.SnapshotPath(c.config.StateDir, command.ForkSnapshotID)
		if err != nil {
			return err
		}
	}
	environment, err := c.claudeEnvironment()
	if err != nil {
		return err
	}
	environment, stopStreamProxy := c.claudeStreamProxy(environment)
	defer stopStreamProxy()
	gitEnvironment, err := c.geminiGitEnvironment(ctx)
	if err != nil {
		return err
	}
	environment = append(environment, gitEnvironment...)
	cliVersion := c.currentClaudeVersion(ctx, effectiveClaudePath(c.config.StateDir))
	command.Questions = command.Questions && buildinfo.SupportsClaudeQuestions(buildinfo.Version, cliVersion)
	command.Steer = command.Steer && buildinfo.SupportsClaudeSteering(buildinfo.Version, cliVersion)
	bridge, closeBridge, bridgeErr := c.sessionBridge(ctx, sessiontools.Ref{Engine: "claude", ID: command.ThreadID}, envelope.OperationID)
	if bridgeErr == nil {
		command.CollaborationMCP = bridge
		defer closeBridge()
	} else {
		c.log.Warn("collaboration bridge unavailable", "engine", "claude")
	}
	forkSnapshotID := command.ForkSnapshotID
	var observedInit bool
	err = c.claudeTurns.Run(ctx, effectiveClaudePath(c.config.StateDir), envelope.OperationID, command, func(event protocol.StreamEvent) error {
		if event.EventID == "" {
			event.EventID = uuid.NewString()
		}
		if event.Kind == protocol.EventClaudeInit {
			observedInit = true
		}
		return c.queue(protocol.AgentKindClaudeEvent, event, true)
	}, environment...)
	if err == nil && observedInit && forkSnapshotID != "" {
		_ = claudeadapter.RemoveSnapshot(c.config.StateDir, forkSnapshotID)
	}
	return err
}

func (c *Client) executeClaudeQuestionAnswer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.ClaudeAnswerCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.claudeTurns.Answer(ctx, command)
}

func (c *Client) executeGeminiQuestionAnswer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.GeminiAnswerCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.gemini.Answer(command)
}

func (c *Client) executeGeminiTurnInterrupt(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.GeminiInterruptCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.interruptGeminiAndWait(ctx, command.OperationID)
}

func (c *Client) executeGeminiTurnSteer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.GeminiSteerCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.gemini.Steer(command)
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
