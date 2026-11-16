package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessiontools"
)

// sessionRunner unifies call entry points while each implementation keeps its
// native wire command, environment, process ownership and completion semantics.
// Codex answers include native approvals; Gemini/Claude interrupts return a
// stopped receipt, while Codex completes through its native event stream.
type sessionRunner interface {
	Start(context.Context, *protocol.ControlEnvelope) error
	Steer(context.Context, *protocol.ControlEnvelope) error
	Interrupt(context.Context, *protocol.ControlEnvelope) (json.RawMessage, error)
	Answer(context.Context, *protocol.ControlEnvelope) error
}

var (
	_ sessionRunner = codexSessionRunner{}
	_ sessionRunner = geminiSessionRunner{}
	_ sessionRunner = claudeSessionRunner{}
)

func (c *Client) sessionRunner(engine string) sessionRunner {
	switch engine {
	case session.Codex:
		return codexSessionRunner{c}
	case session.Gemini:
		return geminiSessionRunner{c}
	case session.Claude:
		return claudeSessionRunner{c}
	default:
		return nil
	}
}

func runSessionInterrupt(ctx context.Context, runner sessionRunner, envelope *protocol.ControlEnvelope) (string, json.RawMessage, bool, error) {
	data, err := runner.Interrupt(ctx, envelope)
	return "", data, false, err
}

// Preserve the previous order: update reservation, permissive native decoding,
// then workspace validation where the Agent previously performed it. Codex
// passes nil because its native adapter owns path validation and bridge setup.
func (c *Client) prepareSessionCommand(raw json.RawMessage, command any, workspace *string) error {
	if c.agentUpdatePending() {
		return errors.New("Agent update is in progress; retry the task after it reconnects")
	}
	if err := json.Unmarshal(raw, command); err != nil {
		return err
	}
	if workspace != nil {
		root, err := scanner.ResolveWorkspaceRoot(*workspace, c.inventoryRoots())
		if err != nil {
			return err
		}
		*workspace = root
	}
	return nil
}

func (c *Client) prepareSessionBridge(ctx context.Context, engine, id, operation string) (map[string]any, func()) {
	bridge, closeBridge, err := c.sessionBridge(ctx, sessiontools.Ref{Engine: engine, ID: id}, operation)
	if err != nil {
		c.log.Warn("collaboration bridge unavailable", "engine", engine)
		return nil, func() {}
	}
	return bridge, closeBridge
}

// emitSessionEvent preserves the old envelopes until the control plane and
// Agent negotiate the replacement in P4-05. All engines use one outbound queue.
func (c *Client) emitSessionEvent(engine string, event protocol.StreamEvent) error {
	kind := ""
	switch engine {
	case session.Codex:
		kind = protocol.AgentKindEvent
	case session.Gemini:
		kind = protocol.AgentKindGeminiEvent
	case session.Claude:
		kind = protocol.AgentKindClaudeEvent
	default:
		return fmt.Errorf("unsupported session engine %q", engine)
	}
	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	return c.queue(kind, event, true)
}
