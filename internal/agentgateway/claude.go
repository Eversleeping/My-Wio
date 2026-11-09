package agentgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func (g *Gateway) handleClaudeEvent(ctx context.Context, serverID string, raw json.RawMessage) error {
	var event protocol.StreamEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	thread, err := g.store.ClaudeThread(ctx, event.StreamID)
	if err != nil {
		return err
	}
	if thread.ServerID != serverID {
		return errors.New("Claude event server mismatch")
	}
	var payload struct {
		OperationID string `json:"operation_id"`
		SessionID   string `json:"session_id"`
		RequestID   string `json:"request_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if payload.OperationID == "" || payload.OperationID != thread.OperationID {
		return nil
	}
	switch event.Kind {
	case "claude.init":
		op, lookupErr := g.store.Operation(ctx, payload.OperationID)
		if lookupErr != nil {
			return lookupErr
		}
		var command protocol.ClaudeTurnCommand
		if json.Unmarshal([]byte(op.Payload), &command) != nil || op.Kind != "claude.turn.start" || op.ServerID != serverID {
			return errors.New("invalid Claude init operation")
		}
		if command.Rewrite != nil {
			replacement, added, err := g.store.CommitClaudeRewrite(ctx, thread.ID, op.ID, payload.SessionID)
			if errors.Is(err, store.ErrThreadActive) {
				return nil
			}
			if err != nil {
				return err
			}
			if added {
				g.hub.Publish(replacement)
			}
			thread.SessionID = payload.SessionID
		}
		if _, err := uuid.Parse(payload.SessionID); err != nil || (thread.SessionID != "" && thread.SessionID != payload.SessionID) || (thread.SessionID == "" && thread.ForkSessionID != "" && thread.ForkSessionID == payload.SessionID) {
			return errors.New("invalid Claude session ID")
		}
		_, err = g.store.DB.ExecContext(ctx, g.store.Q(`UPDATE claude_threads SET session_id=?,status=CASE WHEN status IN ('interrupting','interrupted') THEN status ELSE 'running' END,updated_at=? WHERE id=? AND operation_id=? AND status IN ('queued','running','interrupting','interrupted') AND EXISTS (SELECT 1 FROM agent_operations WHERE id=? AND status IN ('queued','delivered','running'))`), payload.SessionID, time.Now().UTC(), thread.ID, payload.OperationID, payload.OperationID)
		if err != nil {
			return err
		}
		if err := g.store.MarkScheduledTaskByOperation(ctx, payload.OperationID, "running", ""); err != nil {
			return err
		}
	case "claude.steer.ready", "claude.steer.closed":
	case "claude.steer.accepted", "claude.steer.applied", "claude.steer.unconfirmed":
		command, lookupErr := g.store.ClaudeSteerCommand(ctx, thread.ID, payload.RequestID)
		if lookupErr != nil || command.ThreadID != thread.ID || command.OperationID != payload.OperationID {
			return nil
		}
		event.EventID = "claude-steer:" + thread.ID + ":" + payload.RequestID + ":" + event.Kind
		data := map[string]any{"operation_id": command.OperationID, "request_id": command.RequestID}
		if event.Kind == "claude.steer.applied" {
			data["text"] = command.Prompt
			data["images"] = command.Images
		}
		if event.Kind == "claude.steer.unconfirmed" {
			data["message"] = protocol.ClaudeSteerUnconfirmedMessage
		}
		event.Payload, _ = json.Marshal(data)
	case "claude.compaction.started", "claude.compaction.completed", "claude.compaction.failed", "claude.question", "claude.question.answered", "claude.question.cancelled", "claude.message", "claude.tool_use", "claude.tool_result", "claude.notice", "claude.result":
	default:
		return errors.New("unsupported Claude event")
	}
	if thread.Status != "queued" && thread.Status != "running" && thread.Status != "interrupting" && !thread.RecoveryPending {
		return nil
	}
	event.Payload = security.RedactJSON(event.Payload)
	return g.publish(ctx, event)
}

func (g *Gateway) completeClaudeOperation(ctx context.Context, op store.Operation, result protocol.OperationResult) error {
	if op.Kind == "claude.question.answer" {
		return nil
	}
	if op.Kind == "claude.turn.steer" {
		if result.Status == "succeeded" {
			return nil
		}
		var command protocol.ClaudeSteerCommand
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
			return err
		}
		kind := "claude.steer.rejected"
		if result.Message == protocol.ClaudeSteerUnconfirmedMessage {
			kind = "claude.steer.unconfirmed"
		}
		raw, _ := json.Marshal(map[string]string{"operation_id": command.OperationID, "request_id": command.RequestID, "message": result.Message})
		return g.publish(ctx, protocol.StreamEvent{EventID: "claude-steer:" + command.ThreadID + ":" + command.RequestID + ":" + kind, StreamID: command.ThreadID, Kind: kind, Payload: security.RedactJSON(raw)})
	}

	if op.Kind == "claude.turn.interrupt" {
		var command protocol.ClaudeInterruptCommand
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
			return err
		}
		if result.Status == "succeeded" {
			var receipt protocol.GeminiInterruptResult
			if json.Unmarshal(result.Data, &receipt) != nil || !receipt.Stopped || receipt.OperationID != command.OperationID {
				return nil
			}
			original, err := g.store.Operation(ctx, command.OperationID)
			if err != nil {
				return err
			}
			stopped := protocol.OperationResult{OperationID: command.OperationID, Status: "cancelled", Message: "Claude process terminated"}
			claimed, err := g.store.CompleteOperationIfActive(ctx, stopped)
			if err != nil {
				return err
			}
			if !claimed {
				completed, err := g.store.Operation(ctx, command.OperationID)
				if err != nil {
					return err
				}
				return g.reconcileCompletedTurn(ctx, completed)
			}
			return g.completeClaudeOperation(ctx, original, stopped)
		}
		if result.Message == protocol.ClaudeOwnershipLostMessage {
			return g.updateClaudeOwnership(ctx, op.ServerID, command.OperationID, false)
		}
		status := "running"
		if command.RecoveryPending {
			status = "interrupted"
		}
		if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE claude_threads SET status=?,updated_at=? WHERE id=? AND operation_id=? AND status='interrupting'"), status, time.Now().UTC(), command.ThreadID, command.OperationID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"operation_id": command.OperationID, "text": "Could not confirm Claude process termination; the task may still be running."})
		return g.publish(ctx, protocol.StreamEvent{EventID: op.ID + ":interrupt-failed", StreamID: command.ThreadID, Kind: "claude.notice", Payload: payload})
	}
	var command protocol.ClaudeTurnCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
		return err
	}
	status := "idle"
	if result.Status != "succeeded" {
		status = "failed"
	}
	if result.Status == "cancelled" || result.Status == "interrupted" {
		status = "interrupted"
	}
	err := g.store.DB.GetContext(ctx, &status, g.store.Q("UPDATE claude_threads SET status=CASE WHEN status='interrupting' THEN 'interrupted' ELSE ? END,updated_at=? WHERE id=? AND operation_id=? RETURNING status"), status, time.Now().UTC(), command.ThreadID, op.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]string{"status": status, "message": result.Message, "operation_id": op.ID})
	if err := g.store.MarkScheduledTaskByOperation(ctx, op.ID, result.Status, result.Message); err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{EventID: op.ID + ":completed", StreamID: command.ThreadID, Kind: "claude.turn.completed", Payload: security.RedactJSON(raw)})
}

func (g *Gateway) updateClaudeOwnership(ctx context.Context, serverID, operationID string, active bool) error {
	op, err := g.store.Operation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.ServerID != serverID || op.Kind != "claude.turn.start" {
		return errors.New("Claude ownership check operation mismatch")
	}
	var command protocol.ClaudeTurnCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
		return err
	}
	tx, err := g.store.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, g.store.Q("UPDATE agent_operations SET status=status WHERE id=? AND server_id=? AND status IN ('delivered','running')"), operationID, serverID)
	if err != nil {
		return err
	}
	if n, err := locked.RowsAffected(); err != nil || n == 0 {
		return err
	}
	status, condition := "interrupted", "status IN ('queued','running','interrupting')"
	if active {
		status, condition = "running", "status='interrupted'"
	}
	changed, err := tx.ExecContext(ctx, g.store.Q("UPDATE claude_threads SET status=?,updated_at=? WHERE id=? AND operation_id=? AND "+condition), status, time.Now().UTC(), command.ThreadID, operationID)
	if err != nil {
		return err
	}
	if n, err := changed.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"operation_id": operationID, "status": status, "recovery_pending": !active})
	return g.publish(ctx, protocol.StreamEvent{StreamID: command.ThreadID, Kind: "claude.turn.ownership", Payload: raw})
}
