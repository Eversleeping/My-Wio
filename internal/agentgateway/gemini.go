package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func (g *Gateway) handleGeminiEvent(ctx context.Context, serverID string, raw json.RawMessage) error {
	var event protocol.StreamEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	thread, err := g.store.GeminiThread(ctx, event.StreamID)
	if err != nil {
		return err
	}
	if thread.ServerID != serverID {
		return errors.New("Gemini event server mismatch")
	}
	var payload struct {
		OperationID string `json:"operation_id"`
		SessionID   string `json:"session_id"`
		Mode        string `json:"approval_mode"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if payload.OperationID == "" || payload.OperationID != thread.OperationID {
		return nil
	}
	switch event.Kind {
	case "gemini.init":
		if payload.SessionID == "" || len(payload.SessionID) > 128 {
			return errors.New("invalid Gemini session id")
		}
	case "gemini.mode":
		if payload.Mode != "default" && payload.Mode != "auto_edit" && payload.Mode != "yolo" && payload.Mode != "plan" {
			return errors.New("invalid Gemini approval mode update")
		}
	case "gemini.message", "gemini.activity", "gemini.commands", "gemini.tool_use", "gemini.tool_result", "gemini.error", "gemini.result", "gemini.question", "gemini.question.answered", "gemini.steer.ready", "gemini.steer.applied", "gemini.goal.progress", "gemini.goal.usage":
	default:
		return errors.New("unsupported Gemini event")
	}
	// Buffered output does not prove the original process is still alive.
	// Preserve it while awaiting an explicit ownership probe response.
	if thread.Status != "queued" && thread.Status != "running" && thread.Status != "interrupting" && !thread.RecoveryPending {
		return nil
	}
	event.Payload = security.RedactJSON(event.Payload)
	switch event.Kind {
	case "gemini.init":
		operation, err := g.store.Operation(ctx, payload.OperationID)
		if err != nil {
			return err
		}
		if operation.Kind == "gemini.turn.rewrite" {
			replacement, changed, err := g.store.CommitGeminiRewrite(ctx, thread.ID, payload.OperationID, payload.SessionID)
			if err != nil {
				return err
			}
			if changed {
				g.hub.Publish(replacement)
			}
		}
		_, err = g.store.DB.ExecContext(ctx, g.store.Q(`UPDATE gemini_threads SET session_id=?,status=CASE WHEN status IN ('interrupting','interrupted') THEN status ELSE 'running' END,updated_at=? WHERE id=? AND operation_id=? AND status IN ('queued','running','interrupting','interrupted') AND EXISTS (SELECT 1 FROM agent_operations WHERE id=? AND status IN ('queued','delivered','running'))`), payload.SessionID, time.Now().UTC(), thread.ID, payload.OperationID, payload.OperationID)
		if err != nil {
			return err
		}
	case "gemini.mode":
		_, err = g.store.DB.ExecContext(ctx, g.store.Q(`UPDATE gemini_threads SET approval_mode=?,updated_at=? WHERE id=? AND operation_id=? AND status IN ('queued','running','interrupting','interrupted') AND EXISTS (SELECT 1 FROM agent_operations WHERE id=? AND status IN ('queued','delivered','running'))`), payload.Mode, time.Now().UTC(), thread.ID, payload.OperationID, payload.OperationID)
		if err != nil {
			return err
		}
	case "gemini.goal.progress":
		var progress protocol.GeminiGoalProgress
		if err := json.Unmarshal(event.Payload, &progress); err != nil {
			return err
		}
		if err := g.store.UpdateGeminiGoalProgress(ctx, thread.ID, progress); err != nil {
			return err
		}
	}
	return g.publish(ctx, event)
}

func (g *Gateway) completeGeminiOperation(ctx context.Context, op store.Operation, result protocol.OperationResult) error {
	if op.Kind == "gemini.turn.steer" {
		if result.Status == "succeeded" {
			return nil
		}
		var command protocol.GeminiSteerCommand
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]string{"operation_id": command.OperationID, "request_id": command.RequestID, "message": result.Message})
		return g.publish(ctx, protocol.StreamEvent{EventID: op.ID + ":rejected", StreamID: command.ThreadID, Kind: "gemini.steer.rejected", Payload: security.RedactJSON(raw)})
	}
	if op.Kind == "gemini.turn.interrupt" {
		var command protocol.GeminiInterruptCommand
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
			return err
		}
		if result.Status == "succeeded" {
			var confirmed protocol.GeminiInterruptResult
			if json.Unmarshal(result.Data, &confirmed) != nil || !confirmed.Stopped || confirmed.OperationID != command.OperationID {
				// Legacy Agents only acknowledge dispatch; their turn result must
				// still arrive before it is safe to release the workspace.
				return nil
			}
			thread, err := g.store.GeminiThread(ctx, command.ThreadID)
			if err != nil {
				return err
			}
			if thread.OperationID != command.OperationID || thread.Status != "interrupting" {
				return nil
			}
			original, err := g.store.Operation(ctx, command.OperationID)
			if err != nil {
				return err
			}
			stopped := protocol.OperationResult{OperationID: command.OperationID, Status: "cancelled", Message: "Gemini process terminated"}
			if err = g.store.CompleteOperation(ctx, stopped); err != nil {
				return err
			}
			completed, err := g.store.Operation(ctx, command.OperationID)
			if err != nil {
				return err
			}
			if completed.Status != "cancelled" {
				return nil
			}
			return g.completeGeminiOperation(ctx, original, stopped)
		}
		if result.Message == protocol.GeminiOwnershipLostMessage {
			return g.markGeminiOwnershipLost(ctx, op.ServerID, command.OperationID)
		}
		// A failed interrupt must not leave the session permanently locked.
		status := "running"
		if command.RecoveryPending {
			status = "interrupted"
		}
		tx, err := g.store.DB.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		changed, err := tx.ExecContext(ctx, g.store.Q("UPDATE gemini_threads SET status=? WHERE id=? AND operation_id=? AND status='interrupting'"), status, command.ThreadID, command.OperationID)
		if err != nil {
			return err
		}
		count, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if count == 1 && !command.RecoveryPending {
			// The original process can still continue. Do not leave its goal
			// displayed as paused or cancelled when stopping it failed.
			if _, err := tx.ExecContext(ctx, g.store.Q("UPDATE gemini_goals SET status='active',updated_at=? WHERE thread_id=? AND operation_id=? AND status IN ('paused','cancelled')"), time.Now().UTC(), command.ThreadID, command.OperationID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"message": result.Message, "severity": "error", "operation_id": command.OperationID})
		return g.publish(ctx, protocol.StreamEvent{EventID: op.ID + ":interrupt-failed", StreamID: command.ThreadID, Kind: "gemini.error", Payload: security.RedactJSON(raw)})
	}
	var command protocol.GeminiTurnCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
		return err
	}
	thread, err := g.store.GeminiThread(ctx, command.ThreadID)
	if err != nil {
		return err
	}
	if thread.OperationID != op.ID {
		return nil
	}
	status := "idle"
	if result.Status != "succeeded" {
		status = "failed"
	}
	if thread.Status == "interrupting" || result.Status == "interrupted" || result.Status == "cancelled" || (result.Status == "failed" && result.Message == context.Canceled.Error()) {
		status = "interrupted"
	}
	if err := g.store.CompleteGeminiTurn(ctx, command.ThreadID, op.ID, status); err != nil {
		return err
	}
	scheduledStatus := result.Status
	if status == "interrupted" {
		scheduledStatus = "cancelled"
	}
	if err := g.store.MarkScheduledTaskByOperation(ctx, op.ID, scheduledStatus, result.Message); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]string{"status": status, "message": result.Message, "operation_id": op.ID})
	return g.publish(ctx, protocol.StreamEvent{EventID: op.ID + ":completed", StreamID: command.ThreadID, Kind: "gemini.turn.completed", Payload: security.RedactJSON(raw)})
}

func (g *Gateway) markGeminiOwnershipLost(ctx context.Context, serverID, operationID string) error {
	return g.updateGeminiOwnership(ctx, serverID, operationID, false)
}

func (g *Gateway) markGeminiOwnershipActive(ctx context.Context, serverID, operationID string) error {
	return g.updateGeminiOwnership(ctx, serverID, operationID, true)
}

func (g *Gateway) updateGeminiOwnership(ctx context.Context, serverID, operationID string, active bool) error {
	op, err := g.store.Operation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.ServerID != serverID || (op.Kind != "gemini.turn.start" && op.Kind != "gemini.turn.rewrite") {
		return errors.New("Gemini ownership check operation mismatch")
	}
	var command protocol.GeminiTurnCommand
	if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
		return err
	}
	tx, err := g.store.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock the operation before its thread, matching interruption and manual
	// confirmation, so a late probe cannot revive a confirmed termination.
	locked, err := tx.ExecContext(ctx, g.store.Q("UPDATE agent_operations SET status=status WHERE id=? AND server_id=? AND status IN ('delivered','running')"), operationID, serverID)
	if err != nil {
		return err
	}
	count, err := locked.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	status, kind, condition := "interrupted", "gemini.turn.ownership_lost", "status IN ('queued','running','interrupting')"
	if active {
		status, kind, condition = "running", "gemini.turn.ownership_active", "status='interrupted'"
	}
	// A lost owner keeps the workspace reserved until the Agent proves the
	// process stopped or supplies its original result.
	changed, err := tx.ExecContext(ctx, g.store.Q("UPDATE gemini_threads SET status=?,updated_at=? WHERE id=? AND operation_id=? AND "+condition), status, time.Now().UTC(), command.ThreadID, operationID)
	if err != nil {
		return err
	}
	count, err = changed.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return tx.Commit()
	}
	if active {
		// An ownership probe can reveal that a process survived a pause or
		// cancel request whose acknowledgement was lost. Reflect reality and
		// allow the user to issue the stop again.
		if _, err := tx.ExecContext(ctx, g.store.Q("UPDATE gemini_goals SET status='active',updated_at=? WHERE thread_id=? AND operation_id=? AND status IN ('paused','cancelled')"), time.Now().UTC(), command.ThreadID, operationID); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(map[string]any{"operation_id": operationID, "status": status, "recovery_pending": !active})
	event := protocol.StreamEvent{EventID: store.NewID(), StreamID: command.ThreadID, Kind: kind, OccurredAt: time.Now().UTC(), Payload: raw}
	if err := tx.GetContext(ctx, &event.Sequence, g.store.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), event.StreamID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, g.store.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(raw)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	g.hub.Publish(event)
	return nil
}
