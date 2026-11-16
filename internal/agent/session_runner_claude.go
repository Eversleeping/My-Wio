package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessionisolation"
)

type claudeSessionRunner struct{ client *Client }

func (r claudeSessionRunner) Steer(ctx context.Context, envelope *protocol.ControlEnvelope) (err error) {
	c := r.client
	var command protocol.ClaudeSteerCommand
	if err = decodeOperationCommand(envelope.PayloadJSON, &command); err == nil {
		err = c.claudeTurns.Steer(ctx, command)
	}

	return
}

func (r claudeSessionRunner) Interrupt(ctx context.Context, envelope *protocol.ControlEnvelope) (resultData json.RawMessage, err error) {
	c := r.client
	var command protocol.ClaudeInterruptCommand
	if err = decodeOperationCommand(envelope.PayloadJSON, &command); err == nil {
		stopContext, stopCancel := context.WithTimeout(ctx, 15*time.Second)
		err = c.interruptClaudeAndWait(stopContext, command.OperationID)
		stopCancel()
		if err == nil {
			resultData, err = json.Marshal(protocol.GeminiInterruptResult{OperationID: command.OperationID, Stopped: true})
		}
	}

	return
}

func (r claudeSessionRunner) Start(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	ctx = c.processContext(ctx)
	var command protocol.ClaudeTurnCommand
	if err := prepareSessionCommand(c, envelope.PayloadJSON, &command, &command.Workspace); err != nil {
		return err
	}
	var err error
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
	ctx = sessionisolation.WithReadPaths(ctx, command.ResumePath)
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
	declared := capabilities.AgentDeclaration("", cliVersion)
	command.Questions = command.Questions && slices.Contains(declared, protocol.CapabilityClaudeQuestions)
	command.Steer = command.Steer && slices.Contains(declared, protocol.CapabilityClaudeSteer)
	bridge, closeBridge := c.prepareSessionBridge(ctx, session.Claude, command.ThreadID, envelope.OperationID)
	command.CollaborationMCP = bridge
	defer closeBridge()
	forkSnapshotID := command.ForkSnapshotID
	var observedInit bool
	err = c.claudeTurns.Run(ctx, effectiveClaudePath(c.config.StateDir), envelope.OperationID, command, func(event protocol.StreamEvent) error {
		if event.Kind == protocol.EventClaudeInit {
			observedInit = true
		}
		return c.emitSessionEvent(session.Claude, event)
	}, environment...)
	if err == nil && observedInit && forkSnapshotID != "" {
		_ = claudeadapter.RemoveSnapshot(c.config.StateDir, forkSnapshotID)
	}
	return err
}

func (r claudeSessionRunner) Answer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.ClaudeAnswerCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.claudeTurns.Answer(ctx, command)
}
