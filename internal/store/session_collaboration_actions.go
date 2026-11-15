package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessionservice/core"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type collaborationTargetContext struct{}

func collaborationTargetID(ctx context.Context) string {
	if id, _ := ctx.Value(collaborationTargetContext{}).(string); id != "" {
		return id
	}
	return NewID()
}

func (s *Store) queueCollaborationOperation(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, kind string, payload any, write bool) (string, error) {
	if write {
		busy, err := s.collaborationWorkspaceBusy(ctx, tx, t.WorkspaceID)
		if err != nil {
			return "", err
		}
		if busy {
			return "", ErrWorkspaceWriteActive
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := NewID()
	w := 0
	if write {
		w = 1
	}
	var threadID any
	if strings.HasPrefix(kind, protocol.CodexKindPrefix) {
		threadID = t.ID
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,project_id,workspace_id,thread_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)"), id, t.ServerID, t.ProjectID, t.WorkspaceID, threadID, kind, string(raw), w, "session-tool:"+id, time.Now().UTC())
	return id, err
}

// QueueCodexTurn gives browser and collaboration calls the same atomic session
// claim, operation and user message, including archived-session checks.
func (s *Store) QueueCodexTurn(ctx context.Context, t Thread, command protocol.StartTurnCommand) (string, protocol.StreamEvent, error) {
	return s.QueueCodexTurnRequest(ctx, t, command, "")
}

func collaborationPermissionRank(engine, mode string) int { return core.PermissionRank(engine, mode) }

func (s *Store) collaborationLimits(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, root string) (int, error) {
	depth := 0
	if root != source.Ref().Key() {
		var d int
		if err := tx.GetContext(ctx, &d, s.Q("SELECT depth FROM session_links WHERE target_key=?"), source.Ref().Key()); err != nil {
			return 0, err
		}
		depth = d
	}
	if depth >= 2 {
		return depth, sessiontools.Fail("DELEGATION_LIMIT", "maximum delegation depth reached")
	}
	var keys []string
	if err := tx.SelectContext(ctx, &keys, s.Q("SELECT target_key FROM session_links WHERE source_key=?"), source.Ref().Key()); err != nil {
		return depth, err
	}
	active := 0
	for _, key := range keys {
		t, err := s.collaborationSession(ctx, tx, refFromKey(key))
		if err == nil && t.Busy() {
			active++
		} else if err == nil || errors.Is(err, sql.ErrNoRows) {
			// A missing session only occupies a slot while its request is still
			// preparing it; a deleted session never counts, even if a stale
			// request row still names it.
			query := "SELECT COUNT(*) FROM session_tool_requests WHERE target_engine=? AND target_id=? AND status IN ('accepted','preparing','queued')"
			if err != nil {
				query = "SELECT COUNT(*) FROM session_tool_requests WHERE target_engine=? AND target_id=? AND phase<>'' AND status IN ('accepted','preparing')"
			}
			var n int
			if err = tx.GetContext(ctx, &n, s.Q(query), refFromKey(key).Engine, refFromKey(key).ID); err != nil {
				return depth, err
			}
			active += n
		} else if err != nil {
			return depth, err
		}
	}
	if active >= 2 {
		return depth, sessiontools.Fail("DELEGATION_LIMIT", "two child sessions are already active")
	}
	return depth, nil
}

func (s *Store) linkCollaboration(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, grant sessiontools.Grant, root string, depth int, r *CollaborationRequest, relation string, auto bool) error {
	ref := sessiontools.Ref{Engine: r.TargetEngine, ID: r.TargetID}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO session_links(target_key,source_key,root_key,depth,request_id,relation) VALUES(?,?,?,?,?,?)"), ref.Key(), source.Ref().Key(), root, depth+1, r.ID, relation); err != nil {
		return err
	}
	child := sessiontools.Grant{}
	if grant.Delegate {
		child = grant
		child.AutoContinue = false
		child.SessionRefs = nil
		child.AllProjectSessions = false
		// Display preferences are chosen per session, never inherited.
		child.ShowUnreadResults, child.ShowCreationResults = false, false
	}
	raw, _ := json.Marshal(child)
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO session_tool_grants(source_key,grant_json,parent_key,parent_version) VALUES(?,?,?,?)"), ref.Key(), string(raw), source.Ref().Key(), r.GrantVersion); err != nil {
		return err
	}
	a := 0
	if auto {
		a = 1
	}
	_, err := tx.ExecContext(ctx, s.Q("INSERT INTO session_subscriptions(target_key,source_key,auto_continue,grant_version) VALUES(?,?,?,?)"), ref.Key(), source.Ref().Key(), a, r.GrantVersion)
	return err
}

func (s *Store) collaborationWorktree(ctx context.Context, tx *sqlx.Tx, w Workspace, p sessiontools.Params, engine string) (*protocol.GitWorktreeCreateCommand, error) {
	branch := strings.TrimSpace(p.Branch)
	if branch == "" {
		branch = engine + "/wio-" + NewID()[:8]
	}
	if strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\x00\r\n \\~^:?*[") || strings.Contains(branch, "..") {
		return nil, sessiontools.Fail("INVALID_ARGUMENT", "invalid worktree branch")
	}
	base := p.BaseRef
	if base == "" {
		if err := tx.GetContext(ctx, &base, s.Q("SELECT default_branch FROM projects WHERE id=?"), w.ProjectID); err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(base, "-") || strings.ContainsAny(base, "\x00\r\n") {
		return nil, sessiontools.Fail("INVALID_ARGUMENT", "invalid base ref")
	}
	name := strings.ReplaceAll(branch, "/", "-")
	portablePath := strings.ReplaceAll(w.Path, "\\", "/")
	return &protocol.GitWorktreeCreateCommand{SourceWorkspaceID: w.ID, TargetWorkspaceID: NewID(), ProjectID: w.ProjectID, SourcePath: w.Path, TargetPath: path.Join(path.Dir(portablePath), path.Base(portablePath)+"-"+name), Branch: branch, BaseRef: base}, nil
}

func (s *Store) mutateCollaboration(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, grant sessiontools.Grant, root string, r *CollaborationRequest, p sessiontools.Params) (sessiontools.Reply, error) {
	return core.Mutate(transactionPort{s, tx}, ctx, source, grant, root, r, p)
}

func (s *Store) collaborationSendLimit(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, target sessiontools.Ref) error {
	var child int
	if err := tx.GetContext(ctx, &child, s.Q("SELECT COUNT(*) FROM session_links WHERE source_key=? AND target_key=?"), source.Ref().Key(), target.Key()); err != nil {
		return err
	}
	if child == 0 {
		return nil
	}
	var refs []string
	if err := tx.SelectContext(ctx, &refs, s.Q("SELECT target_key FROM session_links WHERE source_key=? AND target_key<>?"), source.Ref().Key(), target.Key()); err != nil {
		return err
	}
	active := 0
	for _, key := range refs {
		t, err := s.collaborationSession(ctx, tx, refFromKey(key))
		if err == nil && t.Busy() {
			active++
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var n int
		r := refFromKey(key)
		if err := tx.GetContext(ctx, &n, s.Q("SELECT COUNT(*) FROM session_tool_requests WHERE target_engine=? AND target_id=? AND phase<>'' AND status IN ('accepted','preparing')"), r.Engine, r.ID); err != nil {
			return err
		}
		if n > 0 {
			active++
		}
	}
	if active >= 2 {
		return sessiontools.Fail("DELEGATION_LIMIT", "two other child sessions are already active")
	}
	return nil
}

// collaborationWorktreeLock reports whether a new worktree from workspace id
// should take its write lock. `git worktree add` leaves the source working tree
// untouched, so when the only active writer is the calling turn itself the
// worktree is created without the lock; otherwise the caller could never wait
// for the child it just delegated. Other writers still defer the request.
func (s *Store) collaborationWorktreeLock(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, id string) (bool, error) {
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=?"), id); err != nil {
		return false, err
	}
	var holders []string
	if err := tx.SelectContext(ctx, &holders, s.Q("SELECT id FROM agent_operations WHERE workspace_id=? AND workspace_write=1 AND status IN ('waiting','queued','delivered','running')"), id); err != nil {
		return false, err
	}
	switch {
	case len(holders) == 0:
		return true, nil
	case len(holders) == 1 && source.OperationID != "" && holders[0] == source.OperationID:
		return false, nil
	default:
		return false, ErrWorkspaceWriteActive
	}
}

func (s *Store) collaborationWorkspaceBusy(ctx context.Context, tx *sqlx.Tx, id string) (bool, error) {
	return s.workspaceWriteBusy(ctx, tx, id, false)
}

// Claude and Gemini turns share a workspace with other turns. All other writers
// remain exclusive. Every writer locks the workspace row before checking and
// inserting its operation so these rules also hold across PostgreSQL connections.
func (s *Store) workspaceWriteBusy(ctx context.Context, tx *sqlx.Tx, id string, sessionTurn bool) (bool, error) {
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=?"), id); err != nil {
		return false, err
	}
	condition := ""
	if sessionTurn {
		condition = " AND kind NOT IN ('claude.turn.start','gemini.turn.start','gemini.turn.rewrite')"
	}
	var count int
	// Staged project deletion reserves the primary workspace while its
	// worktrees are being removed, before its own operation becomes queued.
	err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND workspace_write=1 AND status IN ('waiting','queued','delivered','running')"+condition), id)
	return count > 0, err
}
