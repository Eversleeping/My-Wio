package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

func (s *Store) QueueClaudeSteer(ctx context.Context, command protocol.ClaudeSteerCommand) (protocol.StreamEvent, string, error) {
	if err := protocol.ValidateClaudeSteer(command); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return protocol.StreamEvent{}, "", err
	}
	defer tx.Rollback()
	event, op, err := s.queueClaudeSteerTx(ctx, tx, command)
	if err != nil {
		return event, op, err
	}
	return event, op, tx.Commit()
}

func (s *Store) queueClaudeSteerTx(ctx context.Context, tx *sqlx.Tx, command protocol.ClaudeSteerCommand) (protocol.StreamEvent, string, error) {
	var err error
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE claude_threads SET status=status WHERE id=?"), command.ThreadID); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	key := "claude-steer:" + command.ThreadID + ":" + command.RequestID
	var existing Operation
	err = tx.GetContext(ctx, &existing, s.Q(operationSelect+" WHERE idempotency_key=?"), key)
	if err == nil {
		var previous protocol.ClaudeSteerCommand
		if json.Unmarshal([]byte(existing.Payload), &previous) != nil || previous.OperationID != command.OperationID || previous.Prompt != command.Prompt || !slices.Equal(previous.Images, command.Images) {
			return protocol.StreamEvent{}, "", errors.New("steering request already has different content")
		}
		return protocol.StreamEvent{}, existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.StreamEvent{}, "", err
	}
	var thread ClaudeThread
	if err = tx.GetContext(ctx, &thread, s.Q(claudeThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), command.ThreadID); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	thread.setSteerCapability()
	if !thread.SteerReady || thread.OperationID != command.OperationID || thread.ArchivedAt != nil || thread.ServerStatus != "online" {
		return protocol.StreamEvent{}, "", errors.New("Claude is not ready to receive instructions for this running task")
	}
	var active int
	if err = tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM agent_operations WHERE id=? AND kind='claude.turn.start' AND status IN ('delivered','running')"), command.OperationID); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	if active != 1 {
		return protocol.StreamEvent{}, "", ErrThreadActive
	}
	var previous []string
	if err = tx.SelectContext(ctx, &previous, s.Q("SELECT payload FROM agent_operations WHERE workspace_id=? AND kind='claude.turn.steer' AND payload LIKE ?"), thread.WorkspaceID, "%"+command.OperationID+"%"); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	if len(previous) >= 32 {
		return protocol.StreamEvent{}, "", errors.New("too many steering instructions in this task")
	}
	textBytes := len(command.Prompt)
	imageBytes := 0
	for _, image := range command.Images {
		imageBytes += len(image.DataURL)
	}
	for _, raw := range previous {
		var c protocol.ClaudeSteerCommand
		if json.Unmarshal([]byte(raw), &c) != nil {
			return protocol.StreamEvent{}, "", errors.New("invalid previous steering instruction")
		}
		textBytes += len(c.Prompt)
		for _, image := range c.Images {
			imageBytes += len(image.DataURL)
		}
	}
	if textBytes > 64<<10 || imageBytes > 6<<20 {
		return protocol.StreamEvent{}, "", errors.New("this task has reached its steering input limit")
	}
	id, now := NewID(), time.Now().UTC()
	raw, _ := json.Marshal(command)
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,'claude.turn.steer',?,?,?)`), id, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(raw), key, now); err != nil {
		return protocol.StreamEvent{}, "", err
	}
	payload, _ := json.Marshal(map[string]any{"operation_id": command.OperationID, "request_id": command.RequestID, "text": command.Prompt, "images": command.Images})
	event := protocol.StreamEvent{EventID: NewID(), StreamID: thread.ID, Kind: "claude.steer.queued", OccurredAt: now, Payload: payload}
	if err = tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), thread.ID); err != nil {
		return event, "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, now, string(payload)); err != nil {
		return event, "", err
	}
	return event, id, nil
}

func (s *Store) ClaudeSteerCommand(ctx context.Context, threadID, requestID string) (protocol.ClaudeSteerCommand, error) {
	var raw string
	err := s.DB.GetContext(ctx, &raw, s.Q("SELECT payload FROM agent_operations WHERE kind='claude.turn.steer' AND idempotency_key=?"), "claude-steer:"+threadID+":"+requestID)
	var command protocol.ClaudeSteerCommand
	if err == nil {
		err = json.Unmarshal([]byte(raw), &command)
	}
	return command, err
}
