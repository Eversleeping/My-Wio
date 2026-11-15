package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
	"strings"
	"time"
)

func (g *Gateway) failCodexControlOperation(ctx context.Context, operation store.Operation, result protocol.OperationResult) error {
	var threadID, kind string
	switch operation.Kind {
	case protocol.OpCodexTurnSteer:
		var command protocol.SteerTurnCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		threadID = command.ThreadID
		kind = protocol.EventCodexSteerFailed
	case protocol.OpCodexTurnInterrupt:
		var command protocol.InterruptTurnCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		threadID = command.ThreadID
		kind = protocol.EventCodexInterruptFailed
	case protocol.OpCodexApproval:
		var command protocol.ApprovalDecisionCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		threadID = command.ThreadID
		kind = protocol.EventCodexApprovalFailed
	case protocol.OpCodexThreadCompact:
		var command protocol.CodexSnapshotCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		threadID = command.ThreadID
		kind = protocol.EventCodexCompactFailed
	}
	if threadID == "" {
		return errors.New("failed Codex control operation has no thread id")
	}
	// Codex can lose the in-memory turn state when the Agent or Codex
	// subprocess reconnects. In that case an interrupt is already complete from
	// the user's perspective: there is no live turn left to stop. Reconcile the
	// persisted Wio session instead of leaving it stuck in "running" forever.
	if operation.Kind == protocol.OpCodexTurnInterrupt && noActiveCodexTurn(result.Message) {
		if err := g.store.SetThreadStatus(ctx, threadID, "interrupted"); err != nil {
			return err
		}
		_ = g.store.MarkScheduledTaskByThread(ctx, threadID, "cancelled", result.Message)
		_ = g.store.ResolvePendingApprovals(ctx, threadID, "cancelled")
		payload, err := json.Marshal(map[string]string{
			"operation_id": result.OperationID,
			"source":       "control",
			"text":         "Codex turn was already inactive; the session was marked interrupted",
		})
		if err != nil {
			return err
		}
		return g.publish(ctx, protocol.StreamEvent{EventID: result.OperationID + ":turn-cancelled", StreamID: threadID, Kind: protocol.EventCodexTurnCancelled, Payload: security.RedactJSON(payload)})
	}
	payload, err := json.Marshal(map[string]string{"operation_id": result.OperationID, "text": result.Message})
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{EventID: result.OperationID + ":control-failed", StreamID: threadID, Kind: kind, Payload: security.RedactJSON(payload)})
}

func noActiveCodexTurn(message string) bool {
	normalized := strings.ToLower(message)
	return strings.Contains(normalized, "no active turn") ||
		strings.Contains(normalized, "turn not found") ||
		strings.Contains(normalized, "thread not found")
}

func (g *Gateway) acceptCodexTurn(ctx context.Context, event protocol.StreamEvent) error {
	var payload struct {
		CodexThreadID      string          `json:"codex_thread_id"`
		EditEventID        string          `json:"edit_event_id"`
		ReplacementID      string          `json:"replacement_event_id"`
		ReplacementPayload json.RawMessage `json:"replacement_payload"`
		CutoffSequence     int64           `json:"cutoff_sequence"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if payload.CodexThreadID == "" {
		return errors.New("accepted Codex turn has no thread id")
	}
	if payload.EditEventID == "" && payload.ReplacementID == "" {
		return g.bindCodexThread(ctx, event)
	}
	if payload.EditEventID == "" || payload.ReplacementID == "" || len(payload.ReplacementPayload) == 0 {
		return errors.New("accepted Codex rewrite has incomplete commit metadata")
	}
	replacement, changed, err := g.store.CommitThreadRewrite(ctx, event.StreamID, payload.CodexThreadID, payload.EditEventID, payload.ReplacementID, payload.ReplacementPayload, payload.CutoffSequence)
	if err != nil {
		return err
	}
	if changed {
		g.hub.Publish(replacement)
	}
	return nil
}

func completedTurnStatus(payload json.RawMessage) string {
	var value struct {
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(payload, &value) != nil {
		return "failed"
	}
	switch value.Turn.Status {
	case "completed":
		return "idle"
	case "interrupted", "cancelled", "failed":
		if value.Turn.Status == "cancelled" {
			return "interrupted"
		}
		return value.Turn.Status
	default:
		return "failed"
	}
}

func (g *Gateway) failCodexTurn(ctx context.Context, operation store.Operation, result protocol.OperationResult) error {
	var threadID string
	if operation.Kind == protocol.OpCodexTurnRewrite {
		var command protocol.RewriteTurnCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		threadID = command.Start.ThreadID
	} else {
		var command protocol.StartTurnCommand
		if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
			return err
		}
		threadID = command.ThreadID
	}
	if threadID == "" {
		return errors.New("Codex turn operation has no thread id")
	}
	// Codex has no operation_id on the thread. Check for a newer start in the
	// same statement so replaying an old failure cannot stop a subsequent turn.
	updated, err := g.store.DB.ExecContext(ctx, g.store.Q(`UPDATE codex_threads SET status='failed',updated_at=? WHERE id=? AND status IN ('queued','running','interrupting','idle','failed') AND NOT EXISTS (SELECT 1 FROM agent_operations WHERE thread_id=? AND kind IN ('codex.turn.start','codex.turn.rewrite') AND id<>? AND created_at>=?)`), time.Now().UTC(), threadID, threadID, operation.ID, operation.CreatedAt)
	if err != nil {
		return err
	}
	if count, err := updated.RowsAffected(); err != nil || count == 0 {
		return err
	}
	if err := g.store.MarkScheduledTaskByOperation(ctx, result.OperationID, "failed", result.Message); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"operation_id": result.OperationID, "text": result.Message})
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{EventID: result.OperationID + ":turn-failed", StreamID: threadID, Kind: protocol.EventCodexTurnFailed, Payload: security.RedactJSON(payload)})
}

func isCodexTurnOperation(kind string) bool {
	return kind == protocol.OpCodexTurnStart || kind == protocol.OpCodexTurnRewrite
}

func (g *Gateway) queueBestEffortInterrupt(ctx context.Context, serverID string, operation store.Operation) error {
	var command protocol.InterruptTurnCommand
	switch operation.Kind {
	case protocol.OpCodexTurnStart:
		var start protocol.StartTurnCommand
		if err := json.Unmarshal([]byte(operation.Payload), &start); err != nil {
			return err
		}
		command = protocol.InterruptTurnCommand{ThreadID: start.ThreadID, CodexThread: start.CodexThread, BestEffort: true}
	case protocol.OpCodexTurnRewrite:
		var rewrite protocol.RewriteTurnCommand
		if err := json.Unmarshal([]byte(operation.Payload), &rewrite); err != nil {
			return err
		}
		command = protocol.InterruptTurnCommand{ThreadID: rewrite.Start.ThreadID, CodexThread: rewrite.Start.CodexThread, BestEffort: true}
	default:
		return nil
	}
	if command.ThreadID == "" {
		return errors.New("Codex turn operation has no thread id")
	}
	if _, err := g.store.QueueResourceOperation(ctx, serverID, protocol.OpCodexTurnInterrupt, command, "codex-cancel:"+operation.ID, store.OperationResource{ProjectID: operation.ProjectID, WorkspaceID: operation.WorkspaceID, ThreadID: operation.ThreadID}, false); err != nil {
		return err
	}
	g.Wake(serverID)
	return nil
}

func (g *Gateway) upsertApproval(ctx context.Context, event protocol.StreamEvent) error {
	var p struct {
		RequestID string          `json:"request_id"`
		Kind      string          `json:"kind"`
		Detail    json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(event.Payload, &p) != nil || p.RequestID == "" {
		return nil
	}
	if len(p.Detail) == 0 {
		p.Detail = event.Payload
	}
	_, err := g.store.DB.ExecContext(ctx, g.store.Q(`INSERT INTO approvals(id,thread_id,request_id,kind,detail,status,expires_at) VALUES(?,?,?,?,?,'pending',?)
		ON CONFLICT(thread_id,request_id) DO UPDATE SET
			kind=excluded.kind,
			detail=excluded.detail,
			status='pending',
			expires_at=excluded.expires_at,
			resolved_at=NULL,
			decision=NULL
		WHERE approvals.detail<>excluded.detail`), store.NewID(), event.StreamID, p.RequestID, p.Kind, string(p.Detail), time.Now().UTC().Add(15*time.Minute))
	return err
}

func (g *Gateway) bindCodexThread(ctx context.Context, event protocol.StreamEvent) error {
	var p struct {
		CodexThreadID string `json:"codex_thread_id"`
	}
	if json.Unmarshal(event.Payload, &p) != nil || p.CodexThreadID == "" {
		return nil
	}
	_, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE codex_threads SET codex_thread_id=?,updated_at=? WHERE id=?"), p.CodexThreadID, time.Now().UTC(), event.StreamID)
	return err
}

func (g *Gateway) updateThreadTitle(ctx context.Context, event protocol.StreamEvent) error {
	var p struct {
		ThreadName *string `json:"threadName"`
	}
	if json.Unmarshal(event.Payload, &p) != nil || p.ThreadName == nil {
		return nil
	}
	title := strings.TrimSpace(*p.ThreadName)
	if title == "" {
		return nil
	}
	return g.store.SetThreadTitle(ctx, event.StreamID, title)
}

func (g *Gateway) setThreadTitleFromFirstResponse(ctx context.Context, threadID string) error {
	var payloads []string
	if err := g.store.DB.SelectContext(ctx, &payloads, g.store.Q("SELECT payload FROM events WHERE stream_id=? AND kind='codex.item.completed' ORDER BY sequence DESC LIMIT 100"), threadID); err != nil {
		return err
	}
	var p struct {
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	for _, payload := range payloads {
		p.Item.Type = ""
		p.Item.Text = ""
		if json.Unmarshal([]byte(payload), &p) != nil || p.Item.Type != "agentMessage" {
			continue
		}
		title := firstResponseTitle(p.Item.Text)
		if title != "" {
			return g.store.SetThreadTitleIfDefault(ctx, threadID, title)
		}
	}
	return nil
}

func firstResponseTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		title := strings.TrimSpace(line)
		if title == "" || strings.HasPrefix(title, "```") {
			continue
		}
		title = strings.TrimSpace(strings.TrimLeft(title, "#>*- "))
		title = strings.TrimSpace(strings.Trim(title, "`*_"))
		if title == "" {
			continue
		}
		runes := []rune(title)
		if len(runes) > 48 {
			title = strings.TrimSpace(string(runes[:48])) + "..."
		}
		return title
	}
	return ""
}
