package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrOperationNotActive = errors.New("operation is no longer active")

// operationInterruptKeyPrefix starts the idempotency key of every interruption
// of an operation; the key ends with the previous interruption's ID, if any.
//
// Interruptions are found by an exact prefix match. A text range such as
// key >= 'operation-interrupt:<id>:' AND key < 'operation-interrupt:<id>;'
// only works under byte-order collation: PostgreSQL's en_US.UTF-8 ignores the
// punctuation the range relies on, so every interruption after the first one
// was invisible there.
const operationInterruptKeyPrefix = "operation-interrupt:"

func operationInterruptKey(operationID string) string {
	return operationInterruptKeyPrefix + operationID + ":"
}

// operationInterruptTarget returns the operation an interruption key belongs
// to. The suffix is empty or an interruption ID, which never contains ':'.
func operationInterruptTarget(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, operationInterruptKeyPrefix)
	if !ok {
		return "", false
	}
	separator := strings.LastIndexByte(rest, ':')
	if separator < 0 {
		return "", false
	}
	return rest[:separator], true
}

// attachOperationInterrupts fills in the latest interruption of each listed
// operation that supports interruption. Interruptions are rare, so one scan
// for them, matched in Go, replaces a lookup per listed operation.
func (s *Store) attachOperationInterrupts(ctx context.Context, operations []OperationSummary) error {
	targets := make(map[string]int)
	servers := make(map[string]struct{})
	var oldest time.Time
	for i := range operations {
		if protocol.CanInterruptOperation(operations[i].Kind) {
			targets[operations[i].ID] = i
			servers[operations[i].ServerID] = struct{}{}
			if oldest.IsZero() || operations[i].CreatedAt.Before(oldest) {
				oldest = operations[i].CreatedAt
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	var interrupts []struct {
		ID        string    `db:"id"`
		Key       string    `db:"idempotency_key"`
		Status    string    `db:"status"`
		Result    string    `db:"result"`
		CreatedAt time.Time `db:"created_at"`
	}
	serverIDs := make([]string, 0, len(servers))
	for serverID := range servers {
		serverIDs = append(serverIDs, serverID)
	}
	// An interruption is queued on its target's server after the target was
	// created, so only those rows can match; the margin absorbs clock steps.
	// Without these bounds every list request would read the whole history.
	query, args, err := sqlx.In("SELECT id,idempotency_key,status,COALESCE(result,'') result,created_at FROM agent_operations WHERE kind='operation.interrupt' AND server_id IN (?) AND created_at>=?", serverIDs, oldest.UTC().Add(-time.Hour))
	if err != nil {
		return err
	}
	if err := s.DB.SelectContext(ctx, &interrupts, s.Q(query), args...); err != nil {
		return err
	}
	type latest struct {
		id        string
		createdAt time.Time
	}
	newest := make(map[int]latest)
	for _, interrupt := range interrupts {
		target, ok := operationInterruptTarget(interrupt.Key)
		if !ok {
			continue
		}
		index, ok := targets[target]
		if !ok {
			continue
		}
		// Same order as RequestOperationInterrupt: created_at, then id.
		if current, seen := newest[index]; seen && (interrupt.CreatedAt.Before(current.createdAt) || (interrupt.CreatedAt.Equal(current.createdAt) && interrupt.ID < current.id)) {
			continue
		}
		newest[index] = latest{id: interrupt.ID, createdAt: interrupt.CreatedAt}
		operations[index].InterruptStatus = interrupt.Status
		operations[index].InterruptError = interrupt.Result
	}
	return nil
}

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
	prefix := operationInterruptKey(id)
	var previous Operation
	err = tx.GetContext(ctx, &previous, s.Q(operationSelect+" WHERE server_id=? AND kind='operation.interrupt' AND substr(idempotency_key,1,?)=? ORDER BY created_at DESC,id DESC LIMIT 1"), target.ServerID, utf8.RuneCountInString(prefix), prefix)
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
