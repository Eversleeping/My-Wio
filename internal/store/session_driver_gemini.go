package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type geminiCollaborationDriver struct{ *Store }

func (s geminiCollaborationDriver) TurnWritesWorkspace() bool { return true }

func (s geminiCollaborationDriver) Create(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error {
	now := time.Now().UTC()
	var result sql.Result
	var err error
	result, err = tx.ExecContext(ctx, s.Q(`INSERT INTO gemini_threads(id,workspace_id,title,model,approval_mode,thinking_level,created_at,updated_at,model_credential_revision) SELECT ?,w.id,?,?,?,?,?,?,`+geminiCredentialRevision+` FROM workspaces w JOIN servers s ON s.id=w.server_id LEFT JOIN server_gemini_credential_profiles gcp ON gcp.server_id=s.id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), ref.ID, title, model, mode, effort, now, now, workspaceID)
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

	return s.showProjectForNewThread(ctx, tx, workspaceID)
}

func (s geminiCollaborationDriver) Send(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt string) (string, error) {
	native, err := s.collaborationGemini(ctx, tx, t.ID)
	if err != nil {
		return "", err
	}
	_, err = s.queueGeminiTurnTx(ctx, tx, native, prompt, t.Model, t.Mode, "", nil, nil, nil, t.Effort)
	if err != nil {
		return "", err
	}
	var op string
	err = tx.GetContext(ctx, &op, s.Q("SELECT operation_id FROM gemini_threads WHERE id=?"), t.ID)
	return op, err
}

func (s geminiCollaborationDriver) Steer(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt, key string) (string, error) {
	native, err := s.collaborationGemini(ctx, tx, t.ID)
	if err != nil {
		return "", err
	}
	if !native.SteerSupported {
		return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "Gemini steering is unavailable")
	}
	_, op, err := s.queueGeminiSteerTx(ctx, tx, t.ID, t.OperationID, key, prompt)
	return op, err
}

func (s geminiCollaborationDriver) Fork(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error) {
	ctx = context.WithValue(ctx, collaborationTargetContext{}, target)
	t, err := s.collaborationGemini(ctx, tx, source.ID)
	if err != nil {
		return "", err
	}
	op, _, err := s.queueGeminiForkTx(ctx, tx, t, eventID, worktree)
	return op, err
}

func (s geminiCollaborationDriver) Interrupt(ctx context.Context, tx *sqlx.Tx, target CollaborationSession) (string, error) {
	var op string
	var err error
	var t GeminiThread
	t, err = s.collaborationGemini(ctx, tx, target.ID)
	if err == nil {
		op, err = s.interruptGeminiTurnTx(ctx, tx, t)
	}
	return op, err
}

func (s geminiCollaborationDriver) Pin(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, at *time.Time) (bool, error) {
	return false, nil
}

func (s geminiCollaborationDriver) SetExecution(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, model, mode, effort string) error {
	_, err := tx.ExecContext(ctx, s.Q("UPDATE gemini_threads SET model=?,approval_mode=?,thinking_level=? WHERE id=?"), model, mode, effort, ref.ID)
	return err
}

func (s geminiCollaborationDriver) DeferredExecution() bool { return false }

func (s geminiCollaborationDriver) TracksNativeTurn(kind string) bool { return false }
