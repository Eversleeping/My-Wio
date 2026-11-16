package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

type claudeSessionRunner struct{ client *Client }

func (r claudeSessionRunner) Steer(ctx context.Context, envelope *protocol.ControlEnvelope) (err error) {
	c := r.client
	var command protocol.ClaudeSteerCommand
	if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
		err = c.claudeTurns.Steer(ctx, command)
	}

	return
}

func (r claudeSessionRunner) Interrupt(ctx context.Context, envelope *protocol.ControlEnvelope) (resultData json.RawMessage, err error) {
	c := r.client
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

func (r claudeSessionRunner) Start(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.ClaudeTurnCommand
	if err := c.prepareSessionCommand(envelope.PayloadJSON, &command, &command.Workspace); err != nil {
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
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.claudeTurns.Answer(ctx, command)
}
