package store

import (
	"context"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessionservice/core"
	"github.com/wio-platform/wio/internal/sessiontools"
)

// transactionPort never starts or commits a transaction. Every operation below
// uses the caller's original transaction/savepoint and lock order.
type transactionPort struct {
	store *Store
	tx    *sqlx.Tx
}

func (b transactionPort) AuthorizeCollaboration(ctx context.Context, source CollaborationSession, grant sessiontools.Grant, target sessiontools.Ref) (CollaborationSession, error) {
	return b.store.authorizeCollaboration(ctx, b.tx, source, grant, target)
}

func (b transactionPort) CollaborationDefaults(ctx context.Context, source CollaborationSession, grant sessiontools.Grant, engine, workspace string) (string, string, string, error) {
	return b.store.collaborationDefaults(ctx, b.tx, source, grant, engine, workspace)
}

func (b transactionPort) CollaborationFork(ctx context.Context, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error) {
	return b.store.collaborationFork(ctx, b.tx, source, target, worktree, eventID)
}

func (b transactionPort) CollaborationLimits(ctx context.Context, source CollaborationSession, root string) (int, error) {
	return b.store.collaborationLimits(ctx, b.tx, source, root)
}

func (b transactionPort) CollaborationSend(ctx context.Context, t CollaborationSession, prompt string) (string, error) {
	return b.store.collaborationSend(ctx, b.tx, t, prompt)
}

func (b transactionPort) CollaborationSendLimit(ctx context.Context, source CollaborationSession, target sessiontools.Ref) error {
	return b.store.collaborationSendLimit(ctx, b.tx, source, target)
}

func (b transactionPort) CollaborationSession(ctx context.Context, ref sessiontools.Ref) (CollaborationSession, error) {
	return b.store.collaborationSession(ctx, b.tx, ref)
}

func (b transactionPort) CollaborationSteer(ctx context.Context, t CollaborationSession, prompt, key string) (string, error) {
	return b.store.collaborationSteer(ctx, b.tx, t, prompt, key)
}

func (b transactionPort) CollaborationWorkspaceGranted(ctx context.Context, source CollaborationSession, g sessiontools.Grant, id string) (bool, error) {
	return b.store.collaborationWorkspaceGranted(ctx, b.tx, source, g, id)
}

func (b transactionPort) CollaborationWorktree(ctx context.Context, w core.Workspace, p sessiontools.Params, engine string) (*protocol.GitWorktreeCreateCommand, error) {
	return b.store.collaborationWorktree(ctx, b.tx, Workspace{ID: w.ID, ProjectID: w.ProjectID, ServerID: w.ServerID, Path: w.Path}, p, engine)
}

func (b transactionPort) CollaborationWorktreeLock(ctx context.Context, source CollaborationSession, id string) (bool, error) {
	return b.store.collaborationWorktreeLock(ctx, b.tx, source, id)
}

func (b transactionPort) CreateCollaborationSessionTx(ctx context.Context, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error {
	return b.store.createCollaborationSessionTx(ctx, b.tx, ref, workspaceID, title, model, mode, effort)
}

func (b transactionPort) LinkCollaboration(ctx context.Context, source CollaborationSession, grant sessiontools.Grant, root string, depth int, r *CollaborationRequest, relation string, auto bool) error {
	return b.store.linkCollaboration(ctx, b.tx, source, grant, root, depth, r, relation, auto)
}

func (b transactionPort) ObserveCollaborationResult(ctx context.Context, source, target CollaborationSession) (bool, error) {
	return b.store.observeCollaborationResult(ctx, b.tx, source, target)
}

func (b transactionPort) QueueCollaborationOperation(ctx context.Context, t CollaborationSession, kind string, payload any, write bool) (string, error) {
	return b.store.queueCollaborationOperation(ctx, b.tx, t, kind, payload, write)
}

func (b transactionPort) WorkspaceWriteBusy(ctx context.Context, id string, sessionTurn bool) (bool, error) {
	return b.store.workspaceWriteBusy(ctx, b.tx, id, sessionTurn)
}

func (b transactionPort) NewID() string { return NewID() }

func (b transactionPort) ActiveError() error { return ErrThreadActive }

func (b transactionPort) WorkspaceBusyError() error { return ErrWorkspaceWriteActive }

func (b transactionPort) ModelOverride(engine, model, effort string, params sessiontools.Params) (string, string, error) {
	return collaborationModelOverride(engine, model, effort, params)
}

func (b transactionPort) ReadyWorkspace(ctx context.Context, id, project string) (core.Workspace, error) {
	var w Workspace
	query := "SELECT * FROM workspaces WHERE id=? AND status='ready'"
	args := []any{id}
	if project != "" {
		query += " AND project_id=?"
		args = append(args, project)
	}
	err := b.tx.GetContext(ctx, &w, b.store.Q(query), args...)
	return core.Workspace{ID: w.ID, ProjectID: w.ProjectID, ServerID: w.ServerID, Path: w.Path}, err
}

func (b transactionPort) AgentVersion(ctx context.Context, id string) (string, error) {
	var v string
	err := b.tx.GetContext(ctx, &v, b.store.Q("SELECT agent_version FROM servers WHERE id=? AND revoked_at IS NULL"), id)
	return v, err
}

func (b transactionPort) SupportsCollaboration(version string) bool {
	return buildinfo.SupportsSessionCollaboration(version)
}

func (b transactionPort) LockServer(ctx context.Context, id string) error {
	_, err := b.tx.ExecContext(ctx, b.store.Q("UPDATE servers SET status=status WHERE id=?"), id)
	return err
}

func (b transactionPort) TurnWritesWorkspace(engine string) bool {
	return b.store.collaborationDriver(engine).TurnWritesWorkspace()
}

func (b transactionPort) Interrupt(ctx context.Context, t CollaborationSession) (string, error) {
	return b.store.collaborationDriver(t.Engine).Interrupt(ctx, b.tx, t)
}

func (b transactionPort) Update(ctx context.Context, t CollaborationSession, tool string, params sessiontools.Params) error {
	return b.store.updateCollaborationTx(ctx, b.tx, t, tool, params)
}

func (s *Store) updateCollaborationTx(ctx context.Context, tx *sqlx.Tx, target CollaborationSession, tool string, p sessiontools.Params) error {
	var err error
	sets := []string{"updated_at=?"}
	args := []any{time.Now().UTC()}
	table := target.Engine + "_threads"
	if tool == "wio_archive_session" {
		sets = append(sets, "archived_at=?")
		args = append(args, time.Now().UTC())
	}
	if tool == "wio_restore_session" {
		sets = append(sets, "archived_at=NULL")
	}
	if p.Title != nil {
		sets = append(sets, "title=?")
		args = append(args, *p.Title)
	}
	if p.Pinned != nil {
		var at *time.Time
		if *p.Pinned {
			now := time.Now().UTC()
			at = &now
		}
		var separate bool
		separate, err = s.collaborationDriver(target.Engine).Pin(ctx, tx, target, at)
		if !separate {
			sets = append(sets, "pinned_at=?")
			args = append(args, at)
		}
	}
	if err == nil {
		args = append(args, target.ID)
		_, err = tx.ExecContext(ctx, s.Q("UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE id=?"), args...)
	}
	return err
}

func (b transactionPort) Operation(ctx context.Context, id string) (core.Operation, error) {
	var op Operation
	err := b.tx.GetContext(ctx, &op, b.store.Q(operationSelect+" WHERE id=?"), id)
	return core.Operation{Status: op.Status, Result: op.Result}, err
}

func (b transactionPort) ValidMode(engine, mode string) bool {
	return validCollaborationMode(engine, mode)
}

func (b transactionPort) SetExecution(ctx context.Context, ref sessiontools.Ref, model, mode, effort string) error {
	return b.store.collaborationDriver(ref.Engine).SetExecution(ctx, b.tx, ref, model, mode, effort)
}

func (b transactionPort) DeferredExecution(engine string) bool {
	return b.store.collaborationDriver(engine).DeferredExecution()
}
