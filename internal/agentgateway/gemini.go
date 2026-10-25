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
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if payload.OperationID == "" || payload.OperationID != thread.OperationID {
		return nil
	}
	if thread.Status != "queued" && thread.Status != "running" && thread.Status != "interrupting" {
		return nil
	}
	switch event.Kind {
	case "gemini.init":
		if payload.SessionID == "" || len(payload.SessionID) > 128 {
			return errors.New("invalid Gemini session id")
		}
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
		_, err = g.store.DB.ExecContext(ctx, g.store.Q(`UPDATE gemini_threads SET session_id=?,status=CASE WHEN status='interrupting' THEN status ELSE 'running' END,updated_at=? WHERE id=? AND operation_id=?`), payload.SessionID, time.Now().UTC(), thread.ID, payload.OperationID)
		if err != nil {
			return err
		}
	case "gemini.message", "gemini.tool_use", "gemini.tool_result", "gemini.error", "gemini.result":
	default:
		return errors.New("unsupported Gemini event")
	}
	event.Payload = security.RedactJSON(event.Payload)
	return g.publish(ctx, event)
}

func (g *Gateway) completeGeminiOperation(ctx context.Context, op store.Operation, result protocol.OperationResult) error {
	if op.Kind == "gemini.turn.interrupt" {
		if result.Status == "succeeded" {
			return nil
		}
		var command protocol.GeminiInterruptCommand
		if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
			return err
		}
		// A failed interrupt must not leave the session permanently locked.
		_, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE gemini_threads SET status='running' WHERE id=? AND operation_id=? AND status='interrupting'"), command.ThreadID, command.OperationID)
		if err != nil {
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
	if thread.Status == "interrupting" {
		status = "interrupted"
	}
	if err := g.store.CompleteGeminiTurn(ctx, command.ThreadID, op.ID, status); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]string{"status": status, "message": result.Message, "operation_id": op.ID})
	return g.publish(ctx, protocol.StreamEvent{EventID: op.ID + ":completed", StreamID: command.ThreadID, Kind: "gemini.turn.completed", Payload: security.RedactJSON(raw)})
}
