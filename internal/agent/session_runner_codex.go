package agent

import (
	"context"
	"encoding/json"

	"github.com/wio-platform/wio/internal/protocol"
)

type codexSessionRunner struct{ client *Client }

func (r codexSessionRunner) Start(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if envelope.Kind == protocol.OpCodexTurnRewrite {
		return r.rewrite(ctx, envelope)
	}
	c := r.client
	var command protocol.StartTurnCommand
	if err := c.prepareSessionCommand(envelope.PayloadJSON, &command, nil); err != nil {
		return err
	}
	command.OperationID = envelope.OperationID
	return c.codex.StartTurn(ctx, command)
}

func (r codexSessionRunner) rewrite(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.RewriteTurnCommand
	if err := c.prepareSessionCommand(envelope.PayloadJSON, &command, nil); err != nil {
		return err
	}
	command.Start.OperationID = envelope.OperationID
	return c.codex.RewriteTurn(ctx, command)
}

func (r codexSessionRunner) Steer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.SteerTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.SteerTurn(ctx, command)
}

func (r codexSessionRunner) Interrupt(ctx context.Context, envelope *protocol.ControlEnvelope) (json.RawMessage, error) {
	c := r.client
	var command protocol.InterruptTurnCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return nil, err
	}
	return nil, c.codex.Interrupt(ctx, command)
}

func (r codexSessionRunner) Answer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.ApprovalDecisionCommand
	if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.Decide(command)
}
