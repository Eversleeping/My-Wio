package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

func (s *Store) AddEvent(ctx context.Context, event protocol.StreamEvent) (protocol.StreamEvent, error) {
	var saved protocol.StreamEvent
	err := s.inTx(ctx, s.addEventTxOptions(), func(tx *sqlx.Tx) error {
		var err error
		saved, err = s.AppendEventTx(ctx, tx, event)
		return err
	})
	return saved, err
}

// AppendEventTx appends using the same sequence allocation as AddEvent.
// PostgreSQL callers must use READ COMMITTED.
func (s *Store) AppendEventTx(ctx context.Context, tx *sqlx.Tx, event protocol.StreamEvent) (protocol.StreamEvent, error) {
	if event.EventID == "" {
		event.EventID = NewID()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	requestedSequence := event.Sequence
	if s.isPostgres() {
		if err := lockEventStream(ctx, tx, event.StreamID); err != nil {
			return event, err
		}
	}
	for attempt := 1; ; attempt++ {
		event.Sequence = requestedSequence
		if requestedSequence == 0 {
			if err := tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), event.StreamID); err != nil {
				return event, err
			}
		}
		result, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(event.Payload))
		if err != nil {
			return event, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return event, err
		}
		if rows > 0 {
			return event, nil
		}
		// Duplicate delivery of the same event keeps its original sequence.
		err = tx.GetContext(ctx, &event.Sequence, s.Q("SELECT sequence FROM events WHERE event_id=? AND stream_id=?"), event.EventID, event.StreamID)
		if err == nil || !errors.Is(err, sql.ErrNoRows) || requestedSequence != 0 || attempt >= maxEventSequenceAttempts {
			return event, err
		}
		// Another writer that does not take the stream lock (a direct
		// "append user message" insert) claimed the allocated sequence;
		// allocate the next free one.
	}
}

func (s *Store) copyForkEventsTx(ctx context.Context, tx *sqlx.Tx, source, target string, cutoff int64) error {
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &events, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence<=? ORDER BY sequence"), source, cutoff); err != nil {
		return err
	}
	if s.isPostgres() {
		if err := lockEventStream(ctx, tx, target); err != nil {
			return err
		}
	}
	const batchSize = 100
	for offset := 0; offset < len(events); offset += batchSize {
		batch := events[offset:min(offset+batchSize, len(events))]
		placeholders := make([]string, 0, len(batch))
		args := make([]any, 0, 6*len(batch))
		for _, event := range batch {
			placeholders = append(placeholders, "(?,?,?,?,?,?)")
			args = append(args, NewID(), target, event.Sequence, event.Kind, event.OccurredAt, event.Payload)
		}
		query := "INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES" + strings.Join(placeholders, ",")
		if _, err := tx.ExecContext(ctx, s.Q(query), args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Events(ctx context.Context, streamID string, after int64, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	var rows []struct {
		EventID    string    `db:"event_id"`
		StreamID   string    `db:"stream_id"`
		Kind       string    `db:"kind"`
		Payload    string    `db:"payload"`
		Sequence   int64     `db:"sequence"`
		OccurredAt time.Time `db:"occurred_at"`
	}
	var err error
	if streamID == "" {
		err = s.DB.SelectContext(ctx, &rows, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE sequence>? ORDER BY occurred_at,sequence LIMIT ?"), after, limit)
	} else {
		err = s.DB.SelectContext(ctx, &rows, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence>? ORDER BY sequence LIMIT ?"), streamID, after, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]protocol.StreamEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, protocol.StreamEvent{EventID: r.EventID, StreamID: r.StreamID, Sequence: r.Sequence, Kind: r.Kind, OccurredAt: r.OccurredAt, Payload: json.RawMessage(r.Payload)})
	}
	return out, nil
}

// EventsBefore returns the most recent events before sequence in ascending sequence order.
func (s *Store) EventsBefore(ctx context.Context, streamID string, before int64, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM (
		SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events
		WHERE stream_id=? AND sequence<?
		ORDER BY sequence DESC LIMIT ?
	) older ORDER BY sequence`), streamID, before, limit)
}

func (s *Store) ConversationEvents(ctx context.Context, streamID string, after int64, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events
		WHERE stream_id=? AND sequence>? AND kind IN ('user.message','codex.item.started','codex.item.completed','codex.error','codex.turn.completed','codex.turn.failed','codex.turn.cancelled','codex.interrupt.failed','codex.approval.failed','codex.compact.failed','codex.steer.failed')
		ORDER BY sequence LIMIT ?`), streamID, after, limit)
}

// ConversationEventsBefore returns the most recent visible conversation events before sequence in ascending sequence order.
func (s *Store) ConversationEventsBefore(ctx context.Context, streamID string, before int64, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM (
		SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events
		WHERE stream_id=? AND sequence<? AND kind IN ('user.message','codex.item.started','codex.item.completed','codex.error','codex.turn.completed','codex.turn.failed','codex.turn.cancelled','codex.interrupt.failed','codex.approval.failed','codex.compact.failed','codex.steer.failed')
		ORDER BY sequence DESC LIMIT ?
	) older ORDER BY sequence`), streamID, before, limit)
}

func (s *Store) RecentConversationEvents(ctx context.Context, streamID string, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM (
		SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events
		WHERE stream_id=? AND kind IN ('user.message','codex.item.started','codex.item.completed','codex.error','codex.turn.completed','codex.turn.failed','codex.turn.cancelled','codex.interrupt.failed','codex.approval.failed','codex.compact.failed','codex.steer.failed')
		ORDER BY sequence DESC LIMIT ?
	) recent ORDER BY sequence`), streamID, limit)
}

func (s *Store) RecentEvents(ctx context.Context, streamID string, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM (
		SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? ORDER BY sequence DESC LIMIT ?
	) recent ORDER BY sequence`), streamID, limit)
}

func (s *Store) eventRows(ctx context.Context, query string, args ...any) ([]protocol.StreamEvent, error) {
	return s.eventRowsQuery(ctx, s.DB, query, args...)
}

func (s *Store) eventRowsQuery(ctx context.Context, db sqlx.QueryerContext, query string, args ...any) ([]protocol.StreamEvent, error) {
	var rows []struct {
		EventID    string    `db:"event_id"`
		StreamID   string    `db:"stream_id"`
		Kind       string    `db:"kind"`
		Payload    string    `db:"payload"`
		Sequence   int64     `db:"sequence"`
		OccurredAt time.Time `db:"occurred_at"`
	}
	if err := sqlx.SelectContext(ctx, db, &rows, query, args...); err != nil {
		return nil, err
	}
	out := make([]protocol.StreamEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, protocol.StreamEvent{EventID: row.EventID, StreamID: row.StreamID, Sequence: row.Sequence, Kind: row.Kind, OccurredAt: row.OccurredAt, Payload: json.RawMessage(row.Payload)})
	}
	return out, nil
}
