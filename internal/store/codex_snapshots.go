package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Allow the Agent's ten-minute execution deadline plus result delivery time.
const CodexSnapshotTimeout = 11 * time.Minute

const codexSnapshotTimeoutMessage = "Codex information refresh timed out. Please refresh to retry."

func IsReadOnlyCodexSnapshotOperation(kind string) bool {
	switch kind {
	case protocol.OpCodexModelsList, protocol.OpCodexSkillsList, protocol.OpCodexMCPList, protocol.OpCodexStatusSnapshot:
		return true
	default:
		return false
	}
}

// ExpireCodexSnapshots recovers lost results and orphaned loading snapshots.
// It never expires turns or write operations, and retains cached information.
func (s *Store) ExpireCodexSnapshots(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := now.UTC().Add(-CodexSnapshotTimeout)
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status='failed',result=?,completed_at=?
		WHERE kind IN ('codex.models.list','codex.skills.list','codex.mcp.list','codex.status.snapshot')
		AND status IN ('queued','delivered','running') AND COALESCE(started_at,delivered_at,created_at)<=?`), codexSnapshotTimeoutMessage, now.UTC(), cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE codex_snapshots SET status='failed',error=?,updated_at=?
		WHERE kind IN ('models.list','skills.list','mcp.list','status.snapshot')
		AND status='loading' AND requested_at<=?
		AND NOT EXISTS (SELECT 1 FROM agent_operations o WHERE o.kind='codex.'||codex_snapshots.kind
			AND o.status IN ('queued','delivered','running')
			AND ((codex_snapshots.scope_type='workspace' AND o.workspace_id=codex_snapshots.scope_id AND o.thread_id IS NULL)
			OR (codex_snapshots.scope_type='thread' AND o.thread_id=codex_snapshots.scope_id)))`), codexSnapshotTimeoutMessage, now.UTC(), cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteCodexSnapshotOperation atomically finishes the operation and updates
// its cache. Late results and results from older refreshes cannot replace it.
func (s *Store) CompleteCodexSnapshotOperation(ctx context.Context, operation Operation, command protocol.CodexSnapshotCommand, result protocol.OperationResult) (bool, error) {
	if !IsReadOnlyCodexSnapshotOperation(operation.Kind) {
		return false, nil
	}
	data, err := operationResultData(result.Data)
	if err != nil {
		return false, err
	}
	var snapshot protocol.CodexCapabilityResult
	if result.Status == "succeeded" {
		if err := json.Unmarshal(result.Data, &snapshot); err != nil {
			return false, err
		}
		if len(snapshot.Data) == 0 {
			snapshot.Data = json.RawMessage(`{}`)
		}
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	claimed, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=?
		WHERE id=? AND kind=? AND status IN ('queued','delivered','running')`), result.Status, result.Message, data, now, operation.ID, operation.Kind)
	if err != nil {
		return false, err
	}
	count, err := claimed.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	var newer int
	if err := tx.GetContext(ctx, &newer, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE kind=? AND created_at>?
		AND ((?='workspace' AND workspace_id=? AND thread_id IS NULL) OR (?='thread' AND thread_id=?))`), operation.Kind, operation.CreatedAt, command.ScopeType, command.ScopeID, command.ScopeType, command.ScopeID); err != nil {
		return false, err
	}
	if newer == 0 && command.ScopeID != "" && (command.ScopeType == "workspace" || command.ScopeType == "thread") {
		kind := strings.TrimPrefix(operation.Kind, protocol.CodexKindPrefix)
		if result.Status == "succeeded" {
			supported := 0
			if snapshot.Supported {
				supported = 1
			}
			_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,data,supported,reason,codex_version,status,error,requested_at,updated_at)
				VALUES(?,?,?,?,?,?,?,'succeeded','',?,?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET
				data=excluded.data,supported=excluded.supported,reason=excluded.reason,codex_version=excluded.codex_version,status='succeeded',error='',updated_at=excluded.updated_at`), command.ScopeType, command.ScopeID, kind, string(snapshot.Data), supported, snapshot.Reason, snapshot.CodexVersion, now, now)
		} else {
			_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,status,error,requested_at,updated_at)
				VALUES(?,?,?,'failed',?,?,?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET status='failed',error=excluded.error,updated_at=excluded.updated_at`), command.ScopeType, command.ScopeID, kind, result.Message, now, now)
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

type CodexSnapshot struct {
	ScopeType    string     `db:"scope_type" json:"scope_type"`
	ScopeID      string     `db:"scope_id" json:"scope_id"`
	Kind         string     `db:"kind" json:"kind"`
	Data         string     `db:"data" json:"-"`
	Supported    int        `db:"supported" json:"supported"`
	Reason       string     `db:"reason" json:"reason"`
	CodexVersion string     `db:"codex_version" json:"codex_version"`
	Status       string     `db:"status" json:"status"`
	Error        string     `db:"error" json:"error"`
	RequestedAt  *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt    *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) CodexSnapshot(ctx context.Context, scopeType, scopeID, kind string) (CodexSnapshot, error) {
	var snapshot CodexSnapshot
	err := s.DB.GetContext(ctx, &snapshot, s.Q("SELECT scope_type,scope_id,kind,data,supported,reason,codex_version,status,error,requested_at,updated_at FROM codex_snapshots WHERE scope_type=? AND scope_id=? AND kind=?"), scopeType, scopeID, kind)
	return snapshot, err
}

func (s *Store) BeginCodexSnapshot(ctx context.Context, scopeType, scopeID, kind string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,status,error,requested_at) VALUES(?,?,?,'loading','',?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET status='loading',error='',requested_at=excluded.requested_at`), scopeType, scopeID, kind, now)
	return err
}

func (s *Store) SaveCodexSnapshot(ctx context.Context, scopeType, scopeID, kind string, result protocol.CodexCapabilityResult) error {
	data := result.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	var valid any
	if err := json.Unmarshal(data, &valid); err != nil {
		return err
	}
	supported := 0
	if result.Supported {
		supported = 1
	}
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,data,supported,reason,codex_version,status,error,requested_at,updated_at) VALUES(?,?,?,?,?,?,?,'succeeded','',?,?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET data=excluded.data,supported=excluded.supported,reason=excluded.reason,codex_version=excluded.codex_version,status='succeeded',error='',updated_at=excluded.updated_at`), scopeType, scopeID, kind, string(data), supported, result.Reason, result.CodexVersion, now, now)
	return err
}

func (s *Store) FailCodexSnapshot(ctx context.Context, scopeType, scopeID, kind, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,status,error,requested_at) VALUES(?,?,?,'failed',?,?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET status='failed',error=excluded.error,updated_at=excluded.requested_at`), scopeType, scopeID, kind, message, time.Now().UTC())
	return err
}
