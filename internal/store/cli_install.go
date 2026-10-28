package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

var ErrCLIInstallBusy = errors.New("finish active turns and AI CLI installations on this server first")

type CLIActiveThread struct {
	ID     string `db:"id" json:"id"`
	Title  string `db:"title" json:"title"`
	Status string `db:"status" json:"status"`
}

type CLIBlockingOperation struct {
	ID     string `db:"id" json:"id"`
	Kind   string `db:"kind" json:"kind"`
	Status string `db:"status" json:"status"`
}

type CLIInstallBlockers struct {
	ActiveThreads      []CLIActiveThread      `json:"active_threads"`
	BlockingOperations []CLIBlockingOperation `json:"blocking_operations"`
}

func (b CLIInstallBlockers) Busy() bool {
	return len(b.ActiveThreads) != 0 || len(b.BlockingOperations) != 0
}

func (s *Store) CLIInstallBlockers(ctx context.Context, serverID, kind string) (CLIInstallBlockers, error) {
	return s.cliInstallBlockers(ctx, s.DB, serverID, kind)
}

func (s *Store) cliInstallBlockers(ctx context.Context, query sqlx.QueryerContext, serverID, kind string) (CLIInstallBlockers, error) {
	out := CLIInstallBlockers{ActiveThreads: []CLIActiveThread{}, BlockingOperations: []CLIBlockingOperation{}}
	table := "codex_threads"
	if kind == "gemini" {
		table = "gemini_threads"
	} else if kind != "codex" {
		return out, errors.New("unsupported AI CLI")
	}
	if err := sqlx.SelectContext(ctx, query, &out.BlockingOperations, s.Q("SELECT id,kind,status FROM agent_operations WHERE server_id=? AND kind IN ('cli.install','codex.update','agent.update','gemini.credentials.configure') AND status IN ('queued','delivered','running') ORDER BY created_at,id"), serverID); err != nil {
		return out, err
	}
	err := sqlx.SelectContext(ctx, query, &out.ActiveThreads, s.Q("SELECT t.id,t.title,t.status FROM "+table+" t JOIN workspaces w ON w.id=t.workspace_id WHERE w.server_id=? AND t.status IN ('queued','running','interrupting') ORDER BY t.updated_at,t.id"), serverID)
	return out, err
}

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
	blockers, err := s.cliInstallBlockers(ctx, tx, serverID, command.Kind)
	if err != nil {
		return "", err
	}
	if blockers.Busy() {
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
