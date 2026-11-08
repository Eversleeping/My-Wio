package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
)

// A Codex start operation ends when app-server accepts the turn, while native
// execution continues through events. Keep that acknowledgment separate from
// the actual turn ending, including when selecting a historical operation.
func (s *Store) collaborationTurnState(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, op Operation) (string, bool, error) {
	if t.Engine != "codex" || (op.Kind != "codex.turn.start" && op.Kind != "codex.turn.rewrite") {
		switch op.Status {
		case "succeeded", "failed", "cancelled":
			return op.Status, true, nil
		case "interrupted":
			return "cancelled", true, nil
		case "delivered":
			return "running", false, nil
		}
		return op.Status, false, nil
	}
	if op.Status == "failed" || op.Status == "cancelled" {
		return op.Status, true, nil
	}
	start, end, err := s.collaborationTurnBounds(ctx, tx, t.ID, op.ID)
	if err != nil {
		return "unknown", false, err
	}
	if start > 0 {
		var event struct {
			Kind    string `db:"kind"`
			Payload string `db:"payload"`
		}
		err = tx.GetContext(ctx, &event, s.Q("SELECT kind,payload FROM events WHERE stream_id=? AND sequence>=? AND (?=0 OR sequence<?) AND kind IN ('codex.turn.completed','codex.turn.failed','codex.turn.cancelled') ORDER BY sequence DESC LIMIT 1"), t.ID, start, end, end)
		if err == nil {
			if event.Kind == "codex.turn.failed" {
				return "failed", true, nil
			}
			if event.Kind == "codex.turn.cancelled" {
				return "cancelled", true, nil
			}
			var payload struct {
				Turn struct {
					Status string `json:"status"`
				} `json:"turn"`
			}
			_ = json.Unmarshal([]byte(event.Payload), &payload)
			switch payload.Turn.Status {
			case "completed":
				return "succeeded", true, nil
			case "failed":
				return "failed", true, nil
			case "interrupted", "cancelled":
				return "cancelled", true, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "unknown", false, err
		}
	}
	if t.OperationID == op.ID {
		if t.RecoveryPending {
			return "unknown", false, nil
		}
		switch t.Status {
		case "running", "interrupting":
			return "running", false, nil
		case "queued":
			return "queued", false, nil
		case "failed":
			return "failed", true, nil
		case "interrupted":
			return "cancelled", true, nil
		case "idle", "completed":
			if op.Status == "succeeded" {
				return "succeeded", true, nil
			}
		}
	}
	return "unknown", false, nil
}
