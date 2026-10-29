package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

// Steers do not take the workspace write lock held by the active turn. Lock
// the thread instead, capturing the exact operation and serializing completion.
func (s *Store) QueueGeminiSteer(ctx context.Context, threadID, expected, requestID, prompt string) (protocol.StreamEvent, string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return protocol.StreamEvent{}, "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE gemini_threads SET status=status WHERE id=?"), threadID); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	var thread GeminiThread
	if err = tx.GetContext(ctx, &thread, s.Q(geminiThreadSelect+" WHERE t.id=?"), time.Now().UTC().Add(-ServerOnlineGracePeriod), threadID); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	key := "gemini-steer:" + threadID + ":" + requestID
	var existing Operation
	err = tx.GetContext(ctx, &existing, s.Q(operationSelect+" WHERE idempotency_key=?"), key)
	if err == nil {
		var command protocol.GeminiSteerCommand
		if json.Unmarshal([]byte(existing.Payload), &command) != nil || command.Prompt != prompt || command.OperationID != expected {
			return protocol.StreamEvent{}, "", errors.New("steering request already has different content")
		}
		return protocol.StreamEvent{}, existing.ID, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.StreamEvent{}, "", err
	}
	if thread.Status != "running" || thread.OperationID != expected || thread.ArchivedAt != nil || thread.ServerStatus != "online" {
		return protocol.StreamEvent{}, "", ErrThreadActive
	}
	var ready string
	if err = tx.GetContext(ctx, &ready, s.Q("SELECT payload FROM events WHERE stream_id=? AND kind='gemini.steer.ready' ORDER BY sequence DESC LIMIT 1"), threadID); err != nil {
		return protocol.StreamEvent{}, "", errors.New("Gemini steering is not ready for this turn")
	}
	var state struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(ready), &state) != nil || state.OperationID != expected {
		return protocol.StreamEvent{}, "", errors.New("Gemini steering is not ready for this turn")
	}
	var finished int
	if err = tx.GetContext(ctx, &finished, s.Q("SELECT count(*) FROM events WHERE stream_id=? AND kind IN ('gemini.result','gemini.error') AND sequence>(SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=? AND kind='gemini.init')"), threadID, threadID); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	if finished != 0 {
		return protocol.StreamEvent{}, "", ErrThreadActive
	}
	// Bound accepted input even before delayed control messages reach the Agent.
	var total int
	if err = tx.GetContext(ctx, &total, s.Q("SELECT count(*) FROM agent_operations WHERE server_id=? AND kind='gemini.turn.steer' AND payload LIKE ?"), thread.ServerID, "%"+expected+"%"); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	if total >= 32 {
		return protocol.StreamEvent{}, "", errors.New("too many steering instructions in this turn")
	}
	id, now := NewID(), time.Now().UTC()
	command := protocol.GeminiSteerCommand{ThreadID: threadID, OperationID: expected, RequestID: requestID, Prompt: prompt}
	raw, _ := json.Marshal(command)
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,'gemini.turn.steer',?,?,?)`), id, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(raw), key, now)
	if err != nil {
		return protocol.StreamEvent{}, "", err
	}
	message, _ := json.Marshal(map[string]any{"text": prompt, "request_id": requestID, "operation_id": expected})
	event := protocol.StreamEvent{EventID: NewID(), StreamID: threadID, Kind: "gemini.steer.queued", OccurredAt: now, Payload: message}
	if err = tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), threadID); err != nil {
		return event, "", err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, threadID, event.Sequence, event.Kind, now, string(message))
	if err != nil {
		return event, "", err
	}
	return event, id, tx.Commit()
}

func (s *Store) geminiSteeringContext(ctx context.Context, tx *sqlx.Tx, thread string, cutoff int64) ([]string, error) {
	var rows []struct {
		Kind    string `db:"kind"`
		Payload string `db:"payload"`
	}
	err := tx.SelectContext(ctx, &rows, s.Q(`SELECT kind,payload FROM events WHERE stream_id=? AND (?=0 OR sequence<?)
 AND sequence>(SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=? AND kind='gemini.user.message' AND (?=0 OR sequence<?))
 AND kind IN ('gemini.steer.queued','gemini.steer.applied') ORDER BY sequence`), thread, cutoff, cutoff, thread, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	texts := map[string]string{}
	applied := map[string]bool{}
	order := []string{}
	for _, row := range rows {
		var p struct {
			RequestID string `json:"request_id"`
			Text      string `json:"text"`
		}
		if json.Unmarshal([]byte(row.Payload), &p) != nil {
			continue
		}
		if row.Kind == "gemini.steer.queued" {
			texts[p.RequestID] = p.Text
			order = append(order, p.RequestID)
		} else {
			applied[p.RequestID] = true
		}
	}
	out := []string{}
	for _, id := range order {
		if applied[id] {
			out = append(out, texts[id])
		}
	}
	return out, nil
}
