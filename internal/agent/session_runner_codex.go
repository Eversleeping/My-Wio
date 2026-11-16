package agent

import (
	"context"
	"encoding/json"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
	"github.com/wio-platform/wio/internal/sessionisolation"
)

type codexSessionRunner struct{ client *Client }

func (r codexSessionRunner) Start(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	if envelope.Kind == protocol.OpCodexTurnRewrite {
		return r.rewrite(ctx, envelope)
	}
	c := r.client
	ctx = c.processContext(ctx)
	var command protocol.StartTurnCommand
	if err := prepareSessionCommand(c, envelope.PayloadJSON, &command, nil); err != nil {
		return err
	}
	if sessionisolation.Enabled(ctx) {
		root, err := scanner.ResolveWorkspaceRoot(command.Workspace, c.inventoryRoots())
		if err != nil {
			return err
		}
		command.Workspace = root
	}
	command.OperationID = envelope.OperationID
	return c.codex.StartTurn(ctx, command)
}

func (r codexSessionRunner) rewrite(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	ctx = c.processContext(ctx)
	var command protocol.RewriteTurnCommand
	if err := prepareSessionCommand(c, envelope.PayloadJSON, &command, nil); err != nil {
		return err
	}
	if sessionisolation.Enabled(ctx) {
		root, err := scanner.ResolveWorkspaceRoot(command.Start.Workspace, c.inventoryRoots())
		if err != nil {
			return err
		}
		command.Start.Workspace = root
	}
	command.Start.OperationID = envelope.OperationID
	return c.codex.RewriteTurn(ctx, command)
}

func (r codexSessionRunner) Steer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.SteerTurnCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.SteerTurn(ctx, command)
}

func (r codexSessionRunner) Interrupt(ctx context.Context, envelope *protocol.ControlEnvelope) (json.RawMessage, error) {
	c := r.client
	var command protocol.InterruptTurnCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return nil, err
	}
	return nil, c.codex.Interrupt(ctx, command)
}

func (r codexSessionRunner) Answer(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	c := r.client
	var command protocol.ApprovalDecisionCommand
	if err := decodeOperationCommand(envelope.PayloadJSON, &command); err != nil {
		return err
	}
	return c.codex.Decide(command)
}
