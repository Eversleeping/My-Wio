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
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
		err = c.interruptOperation(stopContext, command)
		stopCancel()
	}

	return
}

func (c *Client) runAgentUpdateOperation(ctx context.Context, envelope *protocol.ControlEnvelope) (restartPath string, resultData json.RawMessage, refreshInventory bool, err error) {
	var command protocol.AgentUpdateCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
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
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		var result protocol.ForkThreadResult
		result, err = c.codex.ForkThread(ctx, command)
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	}

	return
}

func (c *Client) executeCodexTurnStart(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if c.agentUpdatePending() {
		return errors.New("Agent update is in progress; retry the task after it reconnects")
	}
	var command protocol.StartTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	command.OperationID = envelope.OperationID
	return c.codex.StartTurn(ctx, command)
}

func (c *Client) executeCodexTurnRewrite(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if c.agentUpdatePending() {
		return errors.New("Agent update is in progress; retry the task after it reconnects")
	}
	var command protocol.RewriteTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	command.Start.OperationID = envelope.OperationID
	return c.codex.RewriteTurn(ctx, command)
}

func (c *Client) executeCodexTurnSteer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.SteerTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.SteerTurn(ctx, command)
}

func (c *Client) executeCodexTurnInterrupt(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.InterruptTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.Interrupt(ctx, command)
}

func (c *Client) executeCodexApproval(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.ApprovalDecisionCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.Decide(command)
}

func (c *Client) executeCredentialsConfigure(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	var command protocol.ConfigureCredentialsCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	scopedKind, _ := protocol.CredentialOperationKind(command.Scope)
	if envelope.Kind != protocol.OpCredentialsConfigure && envelope.Kind != scopedKind {
		return errors.New("credential update scope mismatch")
	}
	return c.configureCredentials(command)
}

func (c *Client) executeCodexUpdate(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if !c.cliInstallMu.TryLock() {
		return errors.New("an AI CLI installation is already running")
	}
	defer c.cliInstallMu.Unlock()
	var command protocol.CodexUpdateCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	if err := c.updateCodexCLI(ctx, command); err != nil {
		return err
	}
	return c.enqueueHeartbeat(ctx)
}

func (c *Client) executeCLIInstall(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if !c.cliInstallMu.TryLock() {
		return errors.New("an AI CLI installation is already running")
	}
	defer c.cliInstallMu.Unlock()
	var command protocol.CLIInstallCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	if err := c.installCLI(ctx, command); err != nil {
		return err
	}
	return c.enqueueHeartbeat(ctx)
}
