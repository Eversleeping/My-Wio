package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrInvalidForkTarget = errors.New("choose the final reply of a finished Codex turn")

type forkEventPayload struct {
	TurnID string `json:"turnId"`
	Item   struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Phase string `json:"phase"`
	} `json:"item"`
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

// ThreadForkBoundary resolves a message against the complete persisted stream,
// including history that the browser has not loaded. Forks stop at turn boundaries.
func (s *Store) ThreadForkBoundary(ctx context.Context, threadID, eventID string) (string, int64, error) {
	var target struct {
		Kind     string `db:"kind"`
		Payload  string `db:"payload"`
		Sequence int64  `db:"sequence"`
	}
	if err := s.DB.GetContext(ctx, &target, s.Q("SELECT kind,payload,sequence FROM events WHERE stream_id=? AND event_id=?"), threadID, eventID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, ErrInvalidForkTarget
		}
		return "", 0, err
	}
	var selected forkEventPayload
	if json.Unmarshal([]byte(target.Payload), &selected) != nil || target.Kind != "codex.item.completed" || selected.Item.Type != "agentMessage" || selected.Item.Phase == "commentary" || selected.TurnID == "" || selected.Item.ID == "" {
		return "", 0, ErrInvalidForkTarget
	}
	events, err := s.eventRows(ctx, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence>? AND kind IN ('codex.item.completed','codex.turn.completed') ORDER BY sequence"), threadID, target.Sequence)
	if err != nil {
		return "", 0, err
	}
	var cutoff int64
	for _, event := range events {
		var value forkEventPayload
		if err := json.Unmarshal(event.Payload, &value); err != nil {
			return "", 0, err
		}
		if event.Kind == "codex.item.completed" && value.TurnID == selected.TurnID && value.Item.Type == "agentMessage" && value.Item.ID != selected.Item.ID {
			return "", 0, ErrInvalidForkTarget
		}
		if event.Kind == "codex.turn.completed" && value.Turn.ID == selected.TurnID && (value.Turn.Status == "completed" || value.Turn.Status == "interrupted" || value.Turn.Status == "failed") {
			if cutoff == 0 {
				cutoff = event.Sequence
			}
		}
	}
	if cutoff > 0 {
		return selected.TurnID, cutoff, nil
	}
	return "", 0, ErrInvalidForkTarget
}

// Verify that a message fork was produced by an Agent that honored its boundary.
func ValidateThreadForkResult(command protocol.ForkThreadCommand, result protocol.ForkThreadResult) error {
	if result.CodexThread == "" || result.CodexThread == command.CodexThread {
		return errors.New("fork must create a new Codex thread")
	}
	if command.LastTurnID != "" && result.LastTurnID != command.LastTurnID {
		return errors.New("Agent did not confirm the requested fork boundary; upgrade the Agent")
	}
	return nil
}
