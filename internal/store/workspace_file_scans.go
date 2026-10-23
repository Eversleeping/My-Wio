package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Allow the Agent's ten-minute execution deadline plus time to return its result.
const WorkspaceFileScanTimeout = 11 * time.Minute

const workspaceFileScanTimeoutMessage = "Workspace file scan timed out. Please refresh to retry."

// ExpireWorkspaceFileScans also repairs snapshots left scanning across restarts.
// Only read-only file scans are expired; Codex turns and writes are unaffected.
func (s *Store) ExpireWorkspaceFileScans(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := now.UTC().Add(-WorkspaceFileScanTimeout)
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations
		SET status='failed',result=?,completed_at=?
		WHERE kind='workspace.files' AND status IN ('queued','delivered','running')
		AND COALESCE(started_at,delivered_at,created_at)<=?`), workspaceFileScanTimeoutMessage, now.UTC(), cutoff); err != nil {
		return err
	}
	// A newer scan may still be active for an old snapshot. Leave it alone.
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE workspace_file_snapshots
		SET status='failed',error=?
		WHERE status='scanning' AND requested_at<=?
		AND NOT EXISTS (SELECT 1 FROM agent_operations o
			WHERE o.workspace_id=workspace_file_snapshots.workspace_id
			AND o.kind='workspace.files' AND o.status IN ('queued','delivered','running'))`), workspaceFileScanTimeoutMessage, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteWorkspaceFileScan claims the operation and saves the snapshot in one
// transaction, so timeout recovery and a late result cannot overwrite each other.
func (s *Store) CompleteWorkspaceFileScan(ctx context.Context, operation Operation, workspaceID string, result protocol.OperationResult) (bool, error) {
	data, err := operationResultData(result.Data)
	if err != nil {
		return false, err
	}
	var files []byte
	var truncated int
	if result.Status == "succeeded" {
		var scan protocol.WorkspaceFilesResult
		if err := json.Unmarshal(result.Data, &scan); err != nil {
			return false, err
		}
		files, err = json.Marshal(scan.Files)
		if err != nil {
			return false, err
		}
		if scan.Truncated {
			truncated = 1
		}
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	claimed, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations
		SET status=?,result=?,result_data=?,completed_at=?
		WHERE id=? AND kind='workspace.files' AND status IN ('queued','delivered','running')`), result.Status, result.Message, data, now, operation.ID)
	if err != nil {
		return false, err
	}
	count, err := claimed.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	// Finish older operations, but only the latest scan may change the snapshot.
	var newer int
	if err := tx.GetContext(ctx, &newer, s.Q(`SELECT COUNT(*) FROM agent_operations
		WHERE workspace_id=? AND kind='workspace.files' AND created_at>?`), workspaceID, operation.CreatedAt); err != nil {
		return false, err
	}
	if newer == 0 {
		if result.Status == "succeeded" {
			_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_snapshots(workspace_id,files,truncated,status,error,requested_at,updated_at)
				VALUES(?,?,?,'succeeded','',?,?) ON CONFLICT(workspace_id) DO UPDATE
				SET files=excluded.files,truncated=excluded.truncated,status='succeeded',error='',updated_at=excluded.updated_at`), workspaceID, string(files), truncated, now, now)
		} else {
			_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_snapshots(workspace_id,status,error,requested_at)
				VALUES(?,'failed',?,?) ON CONFLICT(workspace_id) DO UPDATE SET status='failed',error=excluded.error`), workspaceID, result.Message, now)
		}
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
