package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrOperationNotActive = errors.New("operation is no longer active")

// Lock the target while choosing an idempotency key. Repeated requests reuse
// the in-flight interruption, while a failed attempt can be retried.
func (s *Store) RequestOperationInterrupt(ctx context.Context, id string) (string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET result=result WHERE id=? AND status IN ('queued','delivered','running')`), id)
	if err != nil {
		return "", err
	}
	count, err := locked.RowsAffected()
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", ErrOperationNotActive
	}
	var target Operation
	if err := tx.GetContext(ctx, &target, s.Q(operationSelect+" WHERE id=?"), id); err != nil {
		return "", err
	}
	if !protocol.CanInterruptOperation(target.Kind) || target.WorkspaceWrite != 0 {
		return "", errors.New("operation does not support interruption")
	}
	prefix := "operation-interrupt:" + id + ":"
	var previous Operation
	err = tx.GetContext(ctx, &previous, s.Q(operationSelect+" WHERE kind='operation.interrupt' AND idempotency_key>=? AND idempotency_key<? ORDER BY created_at DESC,id DESC LIMIT 1"), prefix, "operation-interrupt:"+id+";")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if previous.Status == "queued" || previous.Status == "delivered" || previous.Status == "running" {
		return previous.ID, tx.Commit()
	}
	payload, err := json.Marshal(protocol.OperationInterruptCommand{OperationID: id, Kind: target.Kind})
	if err != nil {
		return "", err
	}
	interruptID := NewID()
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at)
		VALUES(?,?,NULLIF(?,''),NULLIF(?,''),'operation.interrupt',?,0,?,?)`), interruptID, target.ServerID, target.ProjectID, target.WorkspaceID, string(payload), prefix+previous.ID, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return interruptID, tx.Commit()
}

// Claim completion before releasing the snapshot, and leave newer requests alone.
func (s *Store) CompleteInterruptedOperation(ctx context.Context, operation Operation, result protocol.OperationResult) (bool, error) {
	if !protocol.CanInterruptOperation(operation.Kind) || result.Status != "cancelled" {
		return false, errors.New("invalid interrupted operation")
	}
	var command struct {
		WorkspaceID string `json:"workspace_id"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return false, err
	}
	if operation.Kind == "workspace.files" {
		return s.CompleteWorkspaceFileScan(ctx, operation, command.WorkspaceID, result)
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status='cancelled',result=?,result_data='{}',completed_at=? WHERE id=? AND status IN ('queued','delivered','running')`), result.Message, time.Now().UTC(), operation.ID)
	if err != nil {
		return false, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	table := ""
	switch operation.Kind {
	case "git.workspace.inspect":
		table = "workspace_git_snapshots"
	case "workspace.files":
		table = "workspace_file_snapshots"
	case "workspace.changes":
		table = "workspace_change_snapshots"
	case "workspace.file.preview":
		table = "workspace_file_previews"
	case "workspace.diff.preview":
		table = "workspace_diff_previews"
	}
	if table != "" {
		query := "UPDATE " + table + " SET status='failed',error=? WHERE workspace_id=? AND requested_at<=? AND status IN ('scanning','refreshing','loading')"
		args := []any{result.Message, command.WorkspaceID, operation.CreatedAt}
		if operation.Kind == "workspace.file.preview" || operation.Kind == "workspace.diff.preview" {
			query += " AND path=?"
			args = append(args, command.Path)
		}
		if _, err := tx.ExecContext(ctx, s.Q(query), args...); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
