package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

func (s *Store) PinClaudeThread(ctx context.Context, id string, pinned bool) error {
	var at *time.Time
	if pinned {
		now := time.Now().UTC()
		at = &now
	}
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO claude_thread_management(thread_id,pinned_at) VALUES(?,?) ON CONFLICT(thread_id) DO UPDATE SET pinned_at=excluded.pinned_at"), id, at)
	return err
}
func (s *Store) ArchiveClaudeProject(ctx context.Context, projectID, serverID string) (int64, error) {
	r, err := s.DB.ExecContext(ctx, s.Q(`UPDATE claude_threads SET archived_at=?,updated_at=? WHERE archived_at IS NULL AND status NOT IN ('queued','running','interrupting') AND (status<>'interrupted' OR NOT EXISTS (SELECT 1 FROM agent_operations WHERE id=claude_threads.operation_id AND status IN ('queued','delivered','running'))) AND workspace_id IN (SELECT id FROM workspaces WHERE project_id=? AND server_id=?)`), time.Now().UTC(), time.Now().UTC(), projectID, serverID)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
func (s *Store) QueueClaudeFork(ctx context.Context, t ClaudeThread, w *protocol.GitWorktreeCreateCommand, eventID ...string) (string, string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	op, target, err := s.queueClaudeForkTx(ctx, tx, t, w, eventID...)
	if err != nil {
		return op, target, err
	}
	return op, target, tx.Commit()
}

func (s *Store) queueClaudeForkTx(ctx context.Context, tx *sqlx.Tx, t ClaudeThread, w *protocol.GitWorktreeCreateCommand, eventID ...string) (string, string, error) {
	var err error
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), t.ServerID); err != nil {
		return "", "", err
	}
	var busy int
	if err = tx.GetContext(ctx, &busy, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('claude.credentials.configure','cli.install','agent.update') AND status IN ('queued','delivered','running')"), t.ServerID); err != nil {
		return "", "", err
	}
	if busy > 0 {
		return "", "", ErrClaudeCredentialsBusy
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=?"), t.WorkspaceID); err != nil {
		return "", "", err
	}
	opID, target := NewID(), collaborationTargetID(ctx)
	r, err := tx.ExecContext(ctx, s.Q(`UPDATE claude_threads SET status='queued',operation_id=? WHERE id=? AND session_id=? AND session_id<>'' AND archived_at IS NULL AND status NOT IN ('queued','running','interrupting') AND (status<>'interrupted' OR NOT EXISTS (SELECT 1 FROM agent_operations WHERE id=claude_threads.operation_id AND status IN ('queued','delivered','running')))`), opID, t.ID, t.SessionID)
	if err != nil {
		return "", "", err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return "", "", ErrThreadActive
	}
	c := protocol.ClaudeForkCommand{SourceThreadID: t.ID, TargetThreadID: target, SessionID: t.SessionID, SourceWorkspace: t.Path, WorkspaceID: t.WorkspaceID, Workspace: t.Path, Title: t.Title + " (continued)"}
	if len(eventID) > 0 && eventID[0] != "" {
		var latest struct {
			ID   string `db:"event_id"`
			Kind string `db:"kind"`
		}
		if err := tx.GetContext(ctx, &latest, s.Q("SELECT event_id,kind FROM events WHERE stream_id=? AND kind IN ('claude.message','claude.user.message','claude.steer.applied') ORDER BY sequence DESC LIMIT 1"), t.ID); err != nil || latest.ID != eventID[0] || latest.Kind != "claude.message" {
			return "", "", ErrInvalidEditTarget
		}
	}
	if err = tx.GetContext(ctx, &c.CutoffSequence, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), t.ID); err != nil {
		return "", "", err
	}
	kind := "claude.thread.fork"
	var payload []byte
	if w != nil {
		c.WorkspaceID = w.TargetWorkspaceID
		c.Workspace = w.TargetPath
		w.ClaudeFork = &c
		kind = "git.worktree.create"
		payload, err = json.Marshal(w)
	} else {
		payload, err = json.Marshal(c)
	}
	if err != nil {
		return "", "", err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,?,?,1,?,?)`), opID, t.ServerID, t.ProjectID, t.WorkspaceID, kind, string(payload), "claude-fork:"+target, time.Now().UTC())
	if err != nil {
		return "", "", err
	}
	return opID, target, nil
}
func (s *Store) commitClaudeFork(ctx context.Context, tx *sqlx.Tx, c protocol.ClaudeForkCommand, snapshot string) error {
	if snapshot != c.TargetThreadID || snapshot == "" {
		return errors.New("invalid Claude snapshot result")
	}
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM claude_threads WHERE id=?"), c.TargetThreadID); err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	now := time.Now().UTC()
	r, err := tx.ExecContext(ctx, s.Q(`INSERT INTO claude_threads(id,workspace_id,title,model,permission_mode,created_at,updated_at) SELECT ?,?,?,model,permission_mode,?,? FROM claude_threads WHERE id=? AND session_id=?`), c.TargetThreadID, c.WorkspaceID, c.Title, now, now, c.SourceThreadID, c.SessionID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_options(thread_id,effort) SELECT ?,COALESCE((SELECT effort FROM claude_thread_options WHERE thread_id=?),'')"), c.TargetThreadID, c.SourceThreadID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_management(thread_id,fork_snapshot_id,fork_session_id) VALUES(?,?,?)"), c.TargetThreadID, snapshot, c.SessionID); err != nil {
		return err
	}
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err = tx.SelectContext(ctx, &events, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence<=? ORDER BY sequence"), c.SourceThreadID, c.CutoffSequence); err != nil {
		return err
	}
	for _, e := range events {
		if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), c.TargetThreadID, e.Sequence, e.Kind, e.OccurredAt, e.Payload); err != nil {
			return err
		}
	}
	return s.showProjectForNewThread(ctx, tx, c.WorkspaceID)
}
func (s *Store) CommitClaudeFork(ctx context.Context, c protocol.ClaudeForkCommand, snapshot string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.commitClaudeFork(ctx, tx, c, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}
