package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

type geminiSessionRunner struct{ client *Client }

func (r geminiSessionRunner) Interrupt(ctx context.Context, envelope *protocol.ControlEnvelope) (resultData json.RawMessage, err error) {
	c := r.client
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

func (r geminiSessionRunner) Start(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.GeminiTurnCommand
	if err := c.prepareSessionCommand(envelope.PayloadJSON, &command, &command.Workspace); err != nil {
		return err
	}
	environment, err := c.geminiGitEnvironment(ctx)
	if err != nil {
		return err
	}
	bridge, closeBridge := c.prepareSessionBridge(ctx, session.Gemini, command.ThreadID, envelope.OperationID)
	command.CollaborationMCP = bridge
	defer closeBridge()
	return c.gemini.RunInTrustedWorkspaceWithEnvironment(ctx, envelope.OperationID, command, environment)
}

func (r geminiSessionRunner) Answer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.GeminiAnswerCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.gemini.Answer(command)
}

func (r geminiSessionRunner) Steer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.GeminiSteerCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.gemini.Steer(command)
}
