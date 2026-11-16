package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/session"
)

var legacyEventEngines = map[string]string{
	protocol.AgentKindEvent:       session.Codex,
	protocol.AgentKindGeminiEvent: session.Gemini,
	protocol.AgentKindClaudeEvent: session.Claude,
}
var legacyOwnershipKinds = map[string]struct {
	engine string
	active bool
}{
	protocol.AgentKindGeminiOwnershipLost:   {session.Gemini, false},
	protocol.AgentKindGeminiOwnershipActive: {session.Gemini, true},
	protocol.AgentKindClaudeOwnershipLost:   {session.Claude, false},
	protocol.AgentKindClaudeOwnershipActive: {session.Claude, true},
}

// Legacy and negotiated envelopes share decoding and native processing. Direct
// engine wrappers remain available to existing callers and regression tests.
func (g *Gateway) handleLegacySessionEvent(ctx context.Context, serverID, engine string, raw json.RawMessage) error {
	var event protocol.StreamEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	return g.handleSessionEvent(ctx, serverID, engine, event)
}

var sessionEventHandlers = map[string]func(*Gateway, context.Context, string, protocol.StreamEvent) error{
	session.Codex:  (*Gateway).processCodexEvent,
	session.Gemini: (*Gateway).processGeminiEvent,
	session.Claude: (*Gateway).processClaudeEvent,
}

func (g *Gateway) handleSessionEvent(ctx context.Context, serverID, engine string, event protocol.StreamEvent) error {
	handler, ok := sessionEventHandlers[engine]
	if !ok {
		return errors.New("unsupported session event engine")
	}
	return handler(g, ctx, serverID, event)
}
func (g *Gateway) handleSessionOwnership(ctx context.Context, serverID, engine, operationID string, active bool) error {
	switch engine {
	case session.Gemini:
		return g.updateGeminiOwnership(ctx, serverID, operationID, active)
	case session.Claude:
		return g.updateClaudeOwnership(ctx, serverID, operationID, active)
	default:
		return errors.New("unsupported session ownership engine")
	}
}
func (g *Gateway) processCodexEvent(ctx context.Context, serverID string, event protocol.StreamEvent) error {
	thread, err := g.store.Thread(ctx, event.StreamID)
	if err != nil {
		return fmt.Errorf("resolve Agent event thread: %w", err)
	}
	if thread.ServerID != serverID {
		return errors.New("Agent event server mismatch")
	}
	if event.Kind == protocol.EventCodexThreadTokenUsageUpdated {
		event.Payload = codexUsagePayload(event.Payload)
	}
	event.Payload = security.RedactJSON(event.Payload)
	if event.Kind != protocol.EventUserMessage {
		cancelled, err := g.store.HasPendingTurnCancellation(ctx, event.StreamID)
		if err != nil {
			return err
		}
		if cancelled {
			// A queued turn can cross the Agent boundary just as the UI
			// cancels it. Its late notifications must not resurrect the
			// cancelled session or leak stale output into the conversation.
			return nil
		}
	}
	if strings.Contains(event.Kind, protocol.EventApprovalRequested) {
		if err := g.upsertApproval(ctx, event); err != nil {
			g.log.Error("could not persist approval request", "thread_id", event.StreamID, "error", err)
		}
	}
	if event.Kind == protocol.EventThreadBound {
		_ = g.bindCodexThread(ctx, event)
	}
	if event.Kind == protocol.EventCodexServerRequestResolved {
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(event.Payload, &params); err != nil {
			return err
		}
		var requestID string
		if json.Unmarshal(params.RequestID, &requestID) != nil {
			requestID = string(params.RequestID)
		}
		if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE approvals SET status='resolved',decision='cancelled',resolved_at=? WHERE thread_id=? AND request_id=? AND status='pending'"), time.Now().UTC(), event.StreamID, requestID); err != nil {
			return err
		}
	}
	if event.Kind == protocol.EventCodexThreadNameUpdated {
		_ = g.updateThreadTitle(ctx, event)
	}
	if event.Kind == protocol.EventCodexTurnStarted || event.Kind == protocol.EventTurnAccepted {
		if event.Kind == protocol.EventTurnAccepted {
			if err := g.acceptCodexTurn(ctx, event); err != nil {
				return err
			}
		}
		saved, err := g.store.AddEvent(ctx, event)
		if err != nil {
			return err
		}
		if err := g.store.SetThreadStatus(ctx, event.StreamID, "running"); err != nil {
			return err
		}
		_ = g.store.MarkScheduledTaskByThread(ctx, event.StreamID, "running", "")
		g.hub.Publish(saved)
		return nil
	}
	if event.Kind == protocol.EventCodexTurnCompleted || event.Kind == protocol.EventCodexTurnCancelled {
		_ = g.setThreadTitleFromFirstResponse(ctx, event.StreamID)
		status := completedTurnStatus(event.Payload)
		if event.Kind == protocol.EventCodexTurnCancelled {
			status = "interrupted"
		}
		_ = g.store.SetThreadStatus(ctx, event.StreamID, status)
		_ = g.store.MarkScheduledTaskByThread(ctx, event.StreamID, scheduledTurnStatus(status), scheduledTurnMessage(event.Payload))
		_ = g.store.ResolvePendingApprovals(ctx, event.StreamID, "cancelled")
	}
	if event.Kind == protocol.EventCodexTurnFailed {
		_ = g.store.SetThreadStatus(ctx, event.StreamID, "failed")
		_ = g.store.MarkScheduledTaskByThread(ctx, event.StreamID, "failed", scheduledTurnMessage(event.Payload))
		_ = g.store.ResolvePendingApprovals(ctx, event.StreamID, "cancelled")
	}
	return g.publish(ctx, event)

}
