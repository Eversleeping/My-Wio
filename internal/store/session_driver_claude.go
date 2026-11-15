package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type claudeCollaborationDriver struct{ *Store }

func (s claudeCollaborationDriver) TurnWritesWorkspace() bool { return true }

func (s claudeCollaborationDriver) Create(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error {
	now := time.Now().UTC()
	var result sql.Result
	var err error
	result, err = tx.ExecContext(ctx, s.Q(`INSERT INTO claude_threads(id,workspace_id,title,model,permission_mode,created_at,updated_at,model_credential_revision) SELECT ?,w.id,?,?,?,?,?,`+claudeModelRevision+` FROM workspaces w JOIN servers s ON s.id=w.server_id LEFT JOIN server_claude_credential_profiles ccp ON ccp.server_id=s.id LEFT JOIN credential_profiles claude ON claude.id=ccp.profile_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), ref.ID, title, model, mode, now, now, workspaceID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_options(thread_id,effort) VALUES(?,?)"), ref.ID, effort); err != nil {
		return err
	}
	return s.showProjectForNewThread(ctx, tx, workspaceID)
}

func (s claudeCollaborationDriver) Send(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt string) (string, error) {
	native, err := s.collaborationClaude(ctx, tx, t.ID)
	if err != nil {
		return "", err
	}
	_, err = s.queueClaudeInputTx(ctx, tx, native, "", prompt, t.Model, t.Mode, t.Effort, nil)
	if err != nil {
		return "", err
	}
	var op string
	err = tx.GetContext(ctx, &op, s.Q("SELECT operation_id FROM claude_threads WHERE id=?"), t.ID)
	return op, err
}

func (s claudeCollaborationDriver) Steer(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt, key string) (string, error) {
	native, err := s.collaborationClaude(ctx, tx, t.ID)
	if err != nil {
		return "", err
	}
	if !native.SteerSupported {
		return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "Claude steering is unavailable")
	}
	_, op, err := s.queueClaudeSteerTx(ctx, tx, protocol.ClaudeSteerCommand{ThreadID: t.ID, OperationID: t.OperationID, RequestID: key, Prompt: prompt})
	return op, err
}

func (s claudeCollaborationDriver) Fork(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error) {
	ctx = context.WithValue(ctx, collaborationTargetContext{}, target)
	t, err := s.collaborationClaude(ctx, tx, source.ID)
	if err != nil {
		return "", err
	}
	op, _, err := s.queueClaudeForkTx(ctx, tx, t, worktree, eventID)
	return op, err
}

func (s claudeCollaborationDriver) Interrupt(ctx context.Context, tx *sqlx.Tx, target CollaborationSession) (string, error) {
	var op string
	var err error
	var t ClaudeThread
	t, err = s.collaborationClaude(ctx, tx, target.ID)
	if err == nil {
		op, err = s.interruptClaudeTurnTx(ctx, tx, t)
	}
	return op, err
}

func (s claudeCollaborationDriver) Pin(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, at *time.Time) (bool, error) {
	_, err := tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_management(thread_id,pinned_at) VALUES(?,?) ON CONFLICT(thread_id) DO UPDATE SET pinned_at=excluded.pinned_at"), t.ID, at)
	return true, err
}

func (s claudeCollaborationDriver) SetExecution(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, model, mode, effort string) error {
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE claude_threads SET model=?,permission_mode=? WHERE id=?"), model, mode, ref.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_options(thread_id,effort) VALUES(?,?) ON CONFLICT(thread_id) DO UPDATE SET effort=excluded.effort"), ref.ID, effort)
	return err
}

func (s claudeCollaborationDriver) DeferredExecution() bool { return false }

func (s claudeCollaborationDriver) TracksNativeTurn(kind string) bool { return false }
