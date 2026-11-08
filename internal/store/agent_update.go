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

var ErrAgentUpdateBusy = errors.New("finish or interrupt active AI tasks and CLI maintenance before updating the Agent")

// ErrAgentUpdateOperationsBusy mirrors the Agent's own refusal: the restart
// would wipe deployments, Git or file writes and other non-idempotent work.
// It also matches ErrAgentUpdateBusy so callers keep mapping it to a conflict.
var ErrAgentUpdateOperationsBusy error = agentUpdateBusyError{message: agentUpdateOperationsBusyMessage}

const agentUpdateOperationsBusyMessage = "finish active deployments, Git and file operations on this server before updating the Agent"

type agentUpdateBusyError struct{ message string }

func (e agentUpdateBusyError) Error() string { return e.message }

func (e agentUpdateBusyError) Is(target error) bool {
	return target == ErrAgentUpdateBusy || target == ErrAgentUpdateOperationsBusy
}

// Every non-turn Agent operation runs under a deadline of at most one hour, so
// a row still active after this long has lost its result. Without the cutoff a
// single stuck row on an Agent too old to reconcile would block the very update
// that fixes it.
const agentUpdateStaleOperationAge = 2 * time.Hour

// AgentUpdateBlockingOperations lists active operations that an Agent restart
// would interrupt. Queued rows always count: they are delivered before the
// update and would then be running when it arrives.
func (s *Store) AgentUpdateBlockingOperations(ctx context.Context, serverID string) ([]CLIBlockingOperation, error) {
	return s.agentUpdateBlockingOperations(ctx, s.DB, serverID, time.Now().UTC())
}

func (s *Store) agentUpdateBlockingOperations(ctx context.Context, query sqlx.QueryerContext, serverID string, now time.Time) ([]CLIBlockingOperation, error) {
	out := []CLIBlockingOperation{}
	statement, args, err := sqlx.In("SELECT id,kind,status FROM agent_operations WHERE server_id=? AND kind NOT IN (?) AND (status='queued' OR (status IN ('delivered','running') AND COALESCE(started_at,delivered_at,created_at)>=?)) ORDER BY created_at,id", serverID, protocol.AgentRestartSafeKinds, now.Add(-agentUpdateStaleOperationAge))
	if err != nil {
		return out, err
	}
	err = sqlx.SelectContext(ctx, query, &out, s.Q(statement), args...)
	return out, err
}

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
	for _, kind := range []string{"codex", "gemini", "claude"} {
		blockers, err := s.cliInstallBlockers(ctx, tx, serverID, kind)
		if err != nil {
			return "", err
		}
		if blockers.Busy() {
			return "", ErrAgentUpdateBusy
		}
	}
	operations, err := s.agentUpdateBlockingOperations(ctx, tx, serverID, time.Now().UTC())
	if err != nil {
		return "", err
	}
	if len(operations) != 0 {
		return "", agentUpdateBusyError{message: fmt.Sprintf("%s (%s is %s)", agentUpdateOperationsBusyMessage, operations[0].Kind, operations[0].Status)}
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
