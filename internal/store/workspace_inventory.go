package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

// Serialize scans, removals and explicit rediscovery on the server row. This
// prevents an inventory already in flight from undoing a completed removal.
func (s *Store) lockWorkspaceInventory(ctx context.Context, tx *sqlx.Tx, serverID string) error {
	result, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET name=name WHERE id=?"), serverID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// QueueProjectDiscovery lets an explicit scan rediscover removed workspaces.
// Background inventory never clears these exclusions. Queueing and clearing
// are atomic so a failed request cannot change a user's visibility choices.
func (s *Store) QueueProjectDiscovery(ctx context.Context, serverID string) (string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := s.lockWorkspaceInventory(ctx, tx, serverID); err != nil {
		return "", err
	}
	operationID := NewID()
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at)
		VALUES(?,?,'inventory.scan','{}',?,?)`), operationID, serverID, "inventory-scan:"+serverID+":"+operationID, time.Now().UTC()); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM workspace_inventory_exclusions WHERE server_id=?"), serverID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
}
