package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"time"
)

func (c *Client) runOperationInterruptOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.OperationInterruptCommand
	if err = decodeOperationCommand(envelope.PayloadJSON, &command); err == nil {
		stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
		err = c.interruptOperation(stopContext, command)
		stopCancel()
	}

	return
}

func (c *Client) runAgentUpdateOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.AgentUpdateCommand
	if err = decodeOperationCommand(envelope.PayloadJSON, &command); err == nil {
		restartPath, err = c.stageAgentUpdate(ctx, command)
	}

	return
}

func (c *Client) runCodexGoalPrefixOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var result protocol.CodexCapabilityResult
	result, err = c.codex.CodexOperation(ctx, envelope.Kind, envelope.PayloadJSON)
	if err == nil && envelope.Kind == protocol.OpCodexThreadCompact && !result.Supported {
		err = errors.New(result.Reason)
	}
	if err == nil {
		resultData, err = json.Marshal(result)
	}

	return
}

func (c *Client) runCodexThreadForkOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.ForkThreadCommand
	if err = decodeOperationCommand(envelope.PayloadJSON, &command); err == nil {
		var result protocol.ForkThreadResult
		result, err = c.codex.ForkThread(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) executeCredentialsConfigure(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.ConfigureCredentialsCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	scopedKind, _ := protocol.CredentialOperationKind(command.Scope)
	if envelope.Kind != protocol.OpCredentialsConfigure && envelope.Kind != scopedKind {
		return errors.New("credential update scope mismatch")
	}
	return c.configureCredentials(command)
}

func (c *Client) executeCodexUpdate(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if !c.cliInstallMu[0].TryLock() {
		return errors.New("an AI CLI installation is already running")
	}
	defer c.cliInstallMu[0].Unlock()
	var command protocol.CodexUpdateCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	if err := c.updateCodexCLI(ctx, command); err != nil {
		return err
	}
	return c.enqueueHeartbeat(ctx)
}

func (c *Client) executeCLIInstall(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.CLIInstallCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	index := -1
	switch command.Kind {
	case "codex":
		index = 0
	case "gemini":
		index = 1
	case "claude":
		index = 2
	default:
		return errors.New("unsupported AI CLI")
	}
	if !c.cliInstallMu[index].TryLock() {
		return errors.New("an installation of this AI CLI is already running")
	}
	defer c.cliInstallMu[index].Unlock()
	if err := c.installCLI(ctx, command); err != nil {
		return err
	}
	return c.enqueueHeartbeat(ctx)
}
