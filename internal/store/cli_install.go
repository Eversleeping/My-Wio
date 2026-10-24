package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrCLIInstallBusy = errors.New("finish active turns and AI CLI installations on this server first")

// Lock the server row so concurrent installation requests cannot both queue.
func (s *Store) QueueCLIInstall(ctx context.Context, serverID string, command protocol.CLIInstallCommand) (string, error) {
	if command.Kind != "codex" && command.Kind != "gemini" {
		return "", errors.New("unsupported AI CLI")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=? AND revoked_at IS NULL"), serverID)
	if err != nil {
		return "", err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return "", errors.New("server unavailable")
	}
	var active int
	if err := tx.GetContext(ctx, &active, s.Q("SELECT count(*) FROM agent_operations WHERE server_id=? AND kind IN ('cli.install','codex.update','agent.update') AND status IN ('queued','delivered','running')"), serverID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrCLIInstallBusy
	}
	table := "codex_threads"
	if command.Kind == "gemini" {
		table = "gemini_threads"
	}
	if err := tx.GetContext(ctx, &active, s.Q("SELECT count(*) FROM "+table+" t JOIN workspaces w ON w.id=t.workspace_id WHERE w.server_id=? AND t.status IN ('queued','running','interrupting')"), serverID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrCLIInstallBusy
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	id := NewID()
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,'cli.install',?,?,?)"), id, serverID, string(payload), "cli-install:"+serverID+":"+id, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *Store) LatestCLIInstall(ctx context.Context, serverID, kind string) (*Operation, error) {
	var operations []Operation
	if err := s.DB.SelectContext(ctx, &operations, s.Q(operationSelect+" WHERE server_id=? AND kind IN ('cli.install','codex.update') ORDER BY created_at DESC LIMIT 100"), serverID); err != nil {
		return nil, err
	}
	for _, op := range operations {
		var command protocol.CLIInstallCommand
		if op.Kind == "codex.update" && kind == "codex" {
			return &op, nil
		}
		if op.Kind == "cli.install" && json.Unmarshal([]byte(op.Payload), &command) == nil && command.Kind == kind {
			return &op, nil
		}
	}
	return nil, nil
}
