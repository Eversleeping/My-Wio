package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type WorkspaceGitSnapshot struct {
	WorkspaceID string                             `json:"workspace_id"`
	Data        protocol.GitWorkspaceInspectResult `json:"data"`
	Status      string                             `json:"status"`
	Error       string                             `json:"error"`
	RequestedAt *time.Time                         `json:"requested_at"`
	UpdatedAt   *time.Time                         `json:"updated_at"`
}

func (s *Store) WorkspaceGitSnapshot(ctx context.Context, workspaceID string) (WorkspaceGitSnapshot, error) {
	var row struct {
		WorkspaceID string     `db:"workspace_id"`
		Data        string     `db:"data"`
		Status      string     `db:"status"`
		Error       string     `db:"error"`
		RequestedAt *time.Time `db:"requested_at"`
		UpdatedAt   *time.Time `db:"updated_at"`
	}
	err := s.DB.GetContext(ctx, &row, s.Q(`SELECT workspace_id,data,status,error,requested_at,updated_at FROM workspace_git_snapshots WHERE workspace_id=?`), workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, workspaceErr := s.Workspace(ctx, workspaceID); workspaceErr != nil {
			return WorkspaceGitSnapshot{}, workspaceErr
		}
		data := protocol.GitWorkspaceInspectResult{WorkspaceID: workspaceID}
		normalizeWorkspaceGitData(&data, workspaceID)
		return WorkspaceGitSnapshot{WorkspaceID: workspaceID, Data: data, Status: "idle"}, nil
	}
	if err != nil {
		return WorkspaceGitSnapshot{}, err
	}
	var data protocol.GitWorkspaceInspectResult
	if err := json.Unmarshal([]byte(row.Data), &data); err != nil {
		return WorkspaceGitSnapshot{}, err
	}
	normalizeWorkspaceGitData(&data, workspaceID)
	return WorkspaceGitSnapshot{WorkspaceID: row.WorkspaceID, Data: data, Status: row.Status, Error: row.Error, RequestedAt: row.RequestedAt, UpdatedAt: row.UpdatedAt}, nil
}

func (s *Store) BeginWorkspaceGitRefresh(ctx context.Context, workspaceID string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_git_snapshots(workspace_id,data,status,error,requested_at) VALUES(?, '{}','refreshing','',?) ON CONFLICT(workspace_id) DO UPDATE SET status='refreshing',error='',requested_at=excluded.requested_at`), workspaceID, now)
	return err
}

func (s *Store) SaveWorkspaceGitSnapshot(ctx context.Context, workspaceID string, result protocol.GitWorkspaceInspectResult) error {
	if result.WorkspaceID != workspaceID {
		return errors.New("workspace Git result does not match command")
	}
	normalizeWorkspaceGitData(&result, workspaceID)
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspace_git_snapshots(workspace_id,data,status,error,requested_at,updated_at) VALUES(?,?,'succeeded','',?,?) ON CONFLICT(workspace_id) DO UPDATE SET data=excluded.data,status='succeeded',error='',updated_at=excluded.updated_at`), workspaceID, string(data), now, now); err != nil {
		return err
	}
	dirty := 0
	if result.Status.Dirty {
		dirty = 1
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE workspaces SET branch=?,commit_sha=?,dirty=?,last_git_refresh_at=?,git_error='' WHERE id=?`), result.Status.Branch, result.Status.Head, dirty, now, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeWorkspaceGitData(data *protocol.GitWorkspaceInspectResult, workspaceID string) {
	if data.WorkspaceID == "" {
		data.WorkspaceID = workspaceID
	}
	if data.Branches == nil {
		data.Branches = make([]protocol.GitBranch, 0)
	}
	if data.Remotes == nil {
		data.Remotes = make([]protocol.GitRemote, 0)
	}
	for index := range data.Remotes {
		if data.Remotes[index].FetchURLs == nil {
			data.Remotes[index].FetchURLs = make([]string, 0)
		}
		if data.Remotes[index].PushURLs == nil {
			data.Remotes[index].PushURLs = make([]string, 0)
		}
	}
	if data.Commits == nil {
		data.Commits = make([]protocol.GitCommit, 0)
	}
	for index := range data.Commits {
		if data.Commits[index].Parents == nil {
			data.Commits[index].Parents = make([]string, 0)
		}
	}
}

func (s *Store) FailWorkspaceGitRefresh(ctx context.Context, workspaceID, message string) error {
	now := time.Now().UTC()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspace_git_snapshots(workspace_id,data,status,error,requested_at,updated_at) VALUES(?, '{}','failed',?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET status='failed',error=excluded.error,updated_at=excluded.updated_at`), workspaceID, message, now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE workspaces SET git_error=?,last_git_refresh_at=? WHERE id=?`), message, now, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}
