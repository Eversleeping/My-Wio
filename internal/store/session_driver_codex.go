package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type codexCollaborationDriver struct{ *Store }

func (s codexCollaborationDriver) TurnWritesWorkspace() bool { return false }

func (s codexCollaborationDriver) Create(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error {
	now := time.Now().UTC()
	var result sql.Result
	var err error
	result, err = tx.ExecContext(ctx, s.Q("INSERT INTO codex_threads(id,workspace_id,title,created_at,updated_at) SELECT ?,w.id,?,?,? FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL"), ref.ID, title, now, now, workspaceID)
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

func (s codexCollaborationDriver) Send(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt string) (string, error) {
	op, _, err := s.queueCodexTurnTx(ctx, tx, Thread{ID: t.ID, ServerID: t.ServerID, ProjectID: t.ProjectID, WorkspaceID: t.WorkspaceID}, protocol.StartTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID, WorkspaceID: t.WorkspaceID, Workspace: t.Path, Prompt: prompt, Model: t.Model, ReasoningEffort: t.Effort, ApprovalMode: t.Mode}, nil)
	return op, err
}

func (s codexCollaborationDriver) Steer(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt, key string) (string, error) {
	return s.collaborationCodexControl(ctx, tx, t, prompt, false)
}

func (s codexCollaborationDriver) Fork(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error) {
	ctx = context.WithValue(ctx, collaborationTargetContext{}, target)
	if source.NativeID == "" || source.Busy() {
		return "", ErrThreadActive
	}
	if worktree != nil {
		if eventID != "" {
			var err error
			worktree.LastTurnID, worktree.CutoffSequence, err = s.threadForkBoundary(ctx, tx, source.ID, eventID)
			if err != nil {
				return "", err
			}
		}
		worktree.SourceThreadID = source.ID
		worktree.TargetThreadID = target
		worktree.CodexThread = source.NativeID
		worktree.Title = source.Title + " (continued)"
		return s.queueCollaborationOperation(ctx, tx, source, protocol.OpGitWorktreeCreate, worktree, true)
	}
	command := protocol.ForkThreadCommand{SourceThreadID: source.ID, TargetThreadID: target, CodexThread: source.NativeID, WorkspaceID: source.WorkspaceID, Workspace: source.Path, Title: source.Title + " (continued)"}
	if eventID != "" {
		var err error
		command.LastTurnID, command.CutoffSequence, err = s.threadForkBoundary(ctx, tx, source.ID, eventID)
		if err != nil {
			return "", err
		}
	}
	return s.queueCollaborationOperation(ctx, tx, source, protocol.OpCodexThreadFork, command, false)
}

func (s codexCollaborationDriver) Interrupt(ctx context.Context, tx *sqlx.Tx, t CollaborationSession) (string, error) {
	var op string
	var err error
	op, err = s.collaborationCodexControl(ctx, tx, t, "", true)
	return op, err
}

func (s codexCollaborationDriver) Pin(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, at *time.Time) (bool, error) {
	return false, nil
}

func (s codexCollaborationDriver) SetExecution(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, model, mode, effort string) error {
	return nil
}

func (s codexCollaborationDriver) DeferredExecution() bool { return true }

func (s codexCollaborationDriver) TracksNativeTurn(kind string) bool {
	return kind == protocol.OpCodexTurnStart || kind == protocol.OpCodexTurnRewrite
}
