package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

func migrateRetentionAndCodexRequests(ctx context.Context, db *sqlx.DB) error {
	_, err := db.ExecContext(ctx, `
 CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions(expires_at);
 CREATE INDEX IF NOT EXISTS metrics_bucket_idx ON metric_rollups(bucket_at);
 CREATE INDEX IF NOT EXISTS operations_kind_project_time_idx ON agent_operations(kind,project_id,created_at,id);
 CREATE INDEX IF NOT EXISTS operations_completed_idx ON agent_operations(completed_at) WHERE status IN ('succeeded','failed','cancelled');
 CREATE INDEX IF NOT EXISTS events_stream_kind_sequence_idx ON events(stream_id,kind,sequence);
 CREATE INDEX IF NOT EXISTS events_operation_time_idx ON events(occurred_at,stream_id) WHERE kind IN ('operation.succeeded','operation.failed','operation.cancelled');
 CREATE TABLE IF NOT EXISTS codex_requests (
 thread_id TEXT NOT NULL REFERENCES codex_threads(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL,
 action TEXT NOT NULL,
 input_hash TEXT NOT NULL,
 operation_id TEXT NOT NULL,
 PRIMARY KEY(thread_id,request_id)
 );`)
	if err != nil {
		return fmt.Errorf("migrate retention indexes and Codex requests: %w", err)
	}
	// Imports queued before resource metadata existed carried the project only
	// in JSON. Backfill that metadata before switching lookups to indexed SQL.
	var legacy []struct {
		ID      string `db:"id"`
		Payload string `db:"payload"`
	}
	if err := db.SelectContext(ctx, &legacy, "SELECT id,payload FROM agent_operations WHERE kind='git.import' AND project_id IS NULL"); err != nil {
		return err
	}
	for _, row := range legacy {
		var command protocol.GitImportCommand
		if json.Unmarshal([]byte(row.Payload), &command) != nil || command.ProjectID == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, db.Rebind(`UPDATE agent_operations SET project_id=? WHERE id=? AND project_id IS NULL AND EXISTS (SELECT 1 FROM projects WHERE id=?) AND (status NOT IN ('queued','delivered') OR NOT EXISTS (SELECT 1 FROM agent_operations active WHERE active.kind='git.import' AND active.project_id=? AND active.server_id=agent_operations.server_id AND active.status IN ('queued','delivered') AND active.id<>agent_operations.id))`), command.ProjectID, row.ID, command.ProjectID, command.ProjectID); err != nil {
			return fmt.Errorf("backfill import project: %w", err)
		}
	}
	return nil
}

// Zero retains audit and operation history indefinitely. Conversation events
// and conversation operations are never subject to retention.
type RetentionPolicy struct {
	AuditLog         time.Duration
	OperationHistory time.Duration
}

const retentionBatchSize = 1000
const monitoringRetention = 7 * 24 * time.Hour

// PruneRetainedData removes bounded batches so a backlog does not monopolize
// SQLite's single connection. Repeated calls continue draining that backlog.
func (s *Store) PruneRetainedData(ctx context.Context, now time.Time, policy RetentionPolicy) error {
	if policy.AuditLog < 0 || policy.OperationHistory < 0 {
		return errors.New("retention periods must not be negative")
	}
	now = now.UTC()
	statements := []struct {
		query  string
		cutoff time.Time
	}{
		{`DELETE FROM sessions WHERE id IN (SELECT id FROM sessions WHERE expires_at<=? LIMIT 1000)`, now},
		{`DELETE FROM metric_rollups WHERE (server_id,bucket_at,resolution) IN (SELECT server_id,bucket_at,resolution FROM metric_rollups WHERE bucket_at<? LIMIT 1000)`, now.Add(-monitoringRetention)},
		// Only known terminal operation notices on server streams. Keep the last
		// sequence as a watermark so pruning cannot restart stream numbering.
		{`DELETE FROM events WHERE event_id IN (SELECT e.event_id FROM events e WHERE e.occurred_at<? AND e.kind IN ('operation.succeeded','operation.failed','operation.cancelled') AND EXISTS (SELECT 1 FROM servers s WHERE s.id=e.stream_id) AND NOT EXISTS (SELECT 1 FROM codex_threads t WHERE t.id=e.stream_id) AND NOT EXISTS (SELECT 1 FROM claude_threads t WHERE t.id=e.stream_id) AND NOT EXISTS (SELECT 1 FROM gemini_threads t WHERE t.id=e.stream_id) AND e.sequence<(SELECT MAX(last.sequence) FROM events last WHERE last.stream_id=e.stream_id) LIMIT 1000)`, now.Add(-monitoringRetention)},
	}
	if policy.AuditLog > 0 {
		statements = append(statements, struct {
			query  string
			cutoff time.Time
		}{`DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE occurred_at<? LIMIT 1000)`, now.Add(-policy.AuditLog)})
	}
	if policy.OperationHistory > 0 {
		// Native session recovery, collaboration and deployments may still depend
		// on operation payloads, even when an operation is terminal.
		statements = append(statements, struct {
			query  string
			cutoff time.Time
		}{`DELETE FROM agent_operations WHERE id IN (SELECT o.id FROM agent_operations o WHERE o.completed_at<? AND o.status IN ('succeeded','failed','cancelled') AND o.thread_id IS NULL AND o.kind NOT LIKE 'codex.%' AND o.kind NOT LIKE 'claude.%' AND o.kind NOT LIKE 'gemini.%' AND NOT EXISTS (SELECT 1 FROM session_tool_requests r WHERE r.operation_id=o.id) AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.operation_id=o.id) AND NOT EXISTS (SELECT 1 FROM deployment_container_state d WHERE d.operation_id=o.id) AND NOT EXISTS (SELECT 1 FROM codex_scheduled_tasks t WHERE t.last_operation_id=o.id) AND NOT EXISTS (SELECT 1 FROM claude_scheduled_tasks t WHERE t.last_operation_id=o.id) AND NOT EXISTS (SELECT 1 FROM gemini_scheduled_tasks t WHERE t.last_operation_id=o.id) LIMIT 1000)`, now.Add(-policy.OperationHistory)})
	}
	for _, statement := range statements {
		for batch := 0; batch < 10; batch++ {
			result, err := s.DB.ExecContext(ctx, s.Q(statement.query), statement.cutoff)
			if err != nil {
				return fmt.Errorf("prune retained data: %w", err)
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n < retentionBatchSize {
				break
			}
		}
	}
	return nil
}
