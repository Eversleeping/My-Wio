package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

func (s *Store) claudeRewriteTarget(ctx context.Context, tx *sqlx.Tx, threadID, eventID string) (protocol.ClaudeRewriteTarget, error) {
	var event struct {
		Payload  string    `db:"payload"`
		Sequence int64     `db:"sequence"`
		At       time.Time `db:"occurred_at"`
	}
	if err := tx.GetContext(ctx, &event, s.Q("SELECT payload,sequence,occurred_at FROM events WHERE stream_id=? AND event_id=? AND kind IN ('claude.user.message','claude.steer.applied')"), threadID, eventID); err != nil {
		return protocol.ClaudeRewriteTarget{}, ErrInvalidEditTarget
	}
	var payload struct {
		Text        string `json:"text"`
		OperationID string `json:"operation_id"`
		RequestID   string `json:"request_id"`
	}
	if json.Unmarshal([]byte(event.Payload), &payload) != nil {
		return protocol.ClaudeRewriteTarget{}, ErrInvalidEditTarget
	}
	id := payload.RequestID
	if id == "" {
		id = payload.OperationID
	}
	if _, err := uuid.Parse(id); err != nil {
		return protocol.ClaudeRewriteTarget{}, ErrInvalidEditTarget
	}
	target := protocol.ClaudeRewriteTarget{EventID: eventID, MessageID: id, Prompt: payload.Text, StartedAt: event.At.UTC().Format(time.RFC3339Nano)}
	var next []time.Time
	if err := tx.SelectContext(ctx, &next, s.Q("SELECT occurred_at FROM events WHERE stream_id=? AND sequence>? AND kind='claude.user.message' ORDER BY sequence LIMIT 1"), threadID, event.Sequence); err != nil {
		return target, err
	}
	if len(next) > 0 {
		target.Before = next[0].UTC().Format(time.RFC3339Nano)
	}
	return target, nil
}

// Retain the original history until the native loader has initialized a new
// session from the prefix. A duplicate init must not truncate the new reply.
func (s *Store) CommitClaudeRewrite(ctx context.Context, threadID, operationID, sessionID string) (protocol.StreamEvent, bool, error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return protocol.StreamEvent{}, false, ErrInvalidEditTarget
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=status WHERE id=? AND kind='claude.turn.start' AND status IN ('queued','delivered','running')"), operationID)
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if count, err := locked.RowsAffected(); err != nil || count != 1 {
		return protocol.StreamEvent{}, false, ErrThreadActive
	}
	var op Operation
	if err := tx.GetContext(ctx, &op, s.Q(operationSelect+" WHERE id=? AND kind='claude.turn.start'"), operationID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	var command protocol.ClaudeTurnCommand
	if json.Unmarshal([]byte(op.Payload), &command) != nil || command.ThreadID != threadID || command.Rewrite == nil || command.SessionID == "" || command.SessionID == sessionID {
		return protocol.StreamEvent{}, false, ErrInvalidEditTarget
	}
	r, err := tx.ExecContext(ctx, s.Q("UPDATE claude_threads SET session_id=?,updated_at=? WHERE id=? AND operation_id=? AND session_id IN (?,?) AND status IN ('queued','running','interrupting','interrupted') AND EXISTS (SELECT 1 FROM agent_operations WHERE id=? AND status IN ('queued','delivered','running'))"), sessionID, time.Now().UTC(), threadID, operationID, command.SessionID, sessionID, operationID)
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return protocol.StreamEvent{}, false, ErrThreadActive
	}
	replacementID := operationID + ":user"
	var exists int
	if err := tx.GetContext(ctx, &exists, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND event_id=?"), threadID, replacementID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if exists > 0 {
		return protocol.StreamEvent{}, false, tx.Commit()
	}
	var sequence, last int64
	if err := tx.GetContext(ctx, &sequence, s.Q("SELECT sequence FROM events WHERE stream_id=? AND event_id=? AND kind IN ('claude.user.message','claude.steer.applied')"), threadID, command.Rewrite.EventID); err != nil {
		return protocol.StreamEvent{}, false, ErrInvalidEditTarget
	}
	if err := tx.GetContext(ctx, &last, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), threadID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=? AND sequence>=?"), threadID, sequence); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	raw, _ := json.Marshal(map[string]any{"text": command.Prompt, "images": command.Images, "operation_id": operationID, "history_rewritten": true, "replaced_sequence": sequence})
	event := protocol.StreamEvent{EventID: replacementID, StreamID: threadID, Sequence: last + 1, Kind: "claude.user.message", OccurredAt: time.Now().UTC(), Payload: raw}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(raw)); err != nil {
		return event, false, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE claude_thread_management SET fork_snapshot_id='',fork_session_id='' WHERE thread_id=?"), threadID); err != nil {
		return event, false, err
	}
	return event, true, tx.Commit()
}
