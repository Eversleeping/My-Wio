package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrApprovalResolved = errors.New("approval was already resolved")

func (s *Store) ResolvePendingApprovals(ctx context.Context, threadID, decision string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE approvals SET status='resolved',decision=?,resolved_at=? WHERE thread_id=? AND status='pending'"), decision, time.Now().UTC(), threadID)
	return err
}

func (s *Store) ResolveApprovalAndQueue(ctx context.Context, approvalID, serverID, decision string, command protocol.ApprovalDecisionCommand) (string, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.Q("UPDATE approvals SET status='resolved',decision=?,resolved_at=? WHERE id=? AND status='pending'"), decision, now, approvalID)
	if err != nil {
		return "", err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if rows != 1 {
		return "", ErrApprovalResolved
	}
	operationID := NewID()
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,thread_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?)"), operationID, serverID, command.ThreadID, protocol.OpCodexApproval, string(payload), "approval:"+approvalID, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
}
