package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrAgentUpdateBusy = errors.New("finish or interrupt active AI tasks and CLI maintenance before updating the Agent")

// Share the server lock with Gemini turns and CLI maintenance so checking for
// active work and queuing a restart cannot race a newly queued Gemini task.
func (s *Store) QueueAgentUpdate(ctx context.Context, serverID string, command protocol.AgentUpdateCommand) (string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=? AND revoked_at IS NULL"), serverID)
	if err != nil {
		return "", err
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return "", errors.New("server unavailable")
	}
	for _, kind := range []string{"codex", "gemini"} {
		blockers, err := s.cliInstallBlockers(ctx, tx, serverID, kind)
		if err != nil {
			return "", err
		}
		if blockers.Busy() {
			return "", ErrAgentUpdateBusy
		}
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	id := NewID()
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,'agent.update',?,?,?)"), id, serverID, string(payload), "agent-update:"+serverID+":"+id, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
