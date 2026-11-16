package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type Port interface {
	AuthorizeCollaboration(ctx context.Context, source CollaborationSession, grant sessiontools.Grant, target sessiontools.Ref) (CollaborationSession, error)
	CollaborationDefaults(ctx context.Context, source CollaborationSession, grant sessiontools.Grant, engine, workspace string) (string, string, string, error)
	CollaborationFork(ctx context.Context, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error)
	CollaborationLimits(ctx context.Context, source CollaborationSession, root string) (int, error)
	CollaborationSend(ctx context.Context, t CollaborationSession, prompt string) (string, error)
	CollaborationSendLimit(ctx context.Context, source CollaborationSession, target sessiontools.Ref) error
	CollaborationSession(ctx context.Context, ref sessiontools.Ref) (CollaborationSession, error)
	CollaborationSteer(ctx context.Context, t CollaborationSession, prompt, key string) (string, error)
	// collaborationWorkspaceGranted resolves the workspace's project before
	// checking the grant, so callers holding only an ID cannot widen the scope.
	CollaborationWorkspaceGranted(ctx context.Context, source CollaborationSession, g sessiontools.Grant, id string) (bool, error)
	CollaborationWorktree(ctx context.Context, w Workspace, p sessiontools.Params, engine string) (*protocol.GitWorktreeCreateCommand, error)
	// collaborationWorktreeLock reports whether a new worktree from workspace id
	// should take its write lock. `git worktree add` leaves the source working tree
	// untouched, so when the only active writer is the calling turn itself the
	// worktree is created without the lock; otherwise the caller could never wait
	// for the child it just delegated. Other writers still defer the request.
	CollaborationWorktreeLock(ctx context.Context, source CollaborationSession, id string) (bool, error)
	CreateCollaborationSessionTx(ctx context.Context, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error
	LinkCollaboration(ctx context.Context, source CollaborationSession, grant sessiontools.Grant, root string, depth int, r *CollaborationRequest, relation string, auto bool) error
	// observeCollaborationResult records that the calling session has seen, during
	// its own turn, that target's latest turn ended. The pending automatic
	// continuation for that turn is then dropped: it would start a new turn only
	// to report what the caller already knows. No continuation is counted.
	ObserveCollaborationResult(ctx context.Context, source, target CollaborationSession) (bool, error)
	QueueCollaborationOperation(ctx context.Context, t CollaborationSession, kind string, payload any, write bool) (string, error)
	// Claude and Gemini turns share a workspace with other turns. All other writers
	// remain exclusive. Every writer locks the workspace row before checking and
	// inserting its operation so these rules also hold across PostgreSQL connections.
	WorkspaceWriteBusy(ctx context.Context, id string, sessionTurn bool) (bool, error)
	NewID() string
	ActiveError() error
	WorkspaceBusyError() error
	ModelOverride(string, string, string, sessiontools.Params) (string, string, error)
	ReadyWorkspace(context.Context, string, string) (Workspace, error)
	SupportsCollaboration(context.Context, string) (bool, error)
	LockServer(context.Context, string) error
	TurnWritesWorkspace(string) bool
	Interrupt(context.Context, CollaborationSession) (string, error)
	Update(context.Context, CollaborationSession, string, sessiontools.Params) error
}

func Mutate(s Port, ctx context.Context, source CollaborationSession, grant sessiontools.Grant, root string, r *CollaborationRequest, p sessiontools.Params) (sessiontools.Reply, error) {
	reply := sessiontools.Reply{RequestID: p.RequestID, Status: "succeeded"}
	if r.Tool == "wio_create_session" || r.Tool == "wio_fork_session" {
		depth, err := s.CollaborationLimits(ctx, source, root)
		if err != nil {
			return reply, err
		}
		if p.AutoContinue && !grant.AutoContinue {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "automatic continuation is not granted")
		}
		origin := source
		if r.Tool == "wio_fork_session" {
			origin, err = s.AuthorizeCollaboration(ctx, source, grant, p.Session)
			if err != nil {
				return reply, err
			}
			if origin.ArchivedAt != nil {
				return reply, sessiontools.Fail("OUT_OF_SCOPE", "restore the source session before forking")
			}
		}
		engine, workspace := p.Engine, p.WorkspaceID
		// A native fork can only continue in the origin's engine and workspace.
		if r.Tool == "wio_fork_session" && p.ForkMode != "handoff" {
			if engine == "" {
				engine = origin.Engine
			}
			if workspace == "" {
				workspace = origin.WorkspaceID
			}
		}
		if engine == "" {
			engine = source.Engine
		}
		if workspace == "" {
			workspace = source.WorkspaceID
		}
		if allowed, e := s.CollaborationWorkspaceGranted(ctx, source, grant, workspace); e != nil {
			return reply, e
		} else if !allowed {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "workspace is not granted")
		}
		w, e := s.ReadyWorkspace(ctx, workspace, source.ProjectID)
		if err = e; err != nil {
			return reply, err
		}
		model, mode, effort, err := s.CollaborationDefaults(ctx, source, grant, engine, workspace)
		if err != nil {
			return reply, err
		}
		execution, _ := json.Marshal(map[string]string{"model": model, "mode": mode, "effort": effort})
		r.Execution = string(execution)
		supported, e := s.SupportsCollaboration(ctx, w.ServerID)
		if err = e; err != nil {
			return reply, err
		}
		if !supported {
			return reply, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "update the target Agent for Wio collaboration")
		}
		r.TargetEngine = engine
		r.TargetID = s.NewID()
		r.WorkspaceID = workspace
		native := r.Tool == "wio_fork_session" && p.ForkMode != "handoff"
		if native && PermissionRank(origin.Engine, origin.Mode) > PermissionRank(source.Engine, source.Mode) {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "native source execution permissions exceed the calling session")
		}
		if native && (engine != origin.Engine || workspace != origin.WorkspaceID) {
			return reply, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "native forks require the same engine and source workspace; choose handoff otherwise")
		}
		if native {
			model, mode, effort = origin.Model, origin.Mode, origin.Effort
		}
		if model, effort, err = s.ModelOverride(engine, model, effort, p); err != nil {
			return reply, err
		}
		execution, _ = json.Marshal(map[string]string{"model": model, "mode": mode, "effort": effort})
		r.Execution = string(execution)
		if r.Tool == "wio_fork_session" && p.ForkMode == "handoff" {
			if strings.TrimSpace(p.Handoff) == "" {
				return reply, sessiontools.Fail("INVALID_ARGUMENT", "handoff requires explicit context")
			}
			p.Prompt = HandoffPrompt(p)
		}
		// Preserve canonical caller arguments for idempotency; generated handoff
		// content is rebuilt when deferred work executes.
		if native && origin.Busy() {
			if p.ForkMode != "after_turn" {
				return reply, sessiontools.Fail("SOURCE_BUSY", "source is running; choose handoff or after_turn")
			}
			r.Phase = "await_source"
			reply.Status = "accepted"
			reply.NextAction = "End the source turn. Do not wait for this fork while the source turn is running."
		} else {
			var worktree *protocol.GitWorktreeCreateCommand
			if p.DirectoryMode == "worktree" {
				if !grant.Worktree {
					return reply, sessiontools.Fail("OUT_OF_SCOPE", "worktree creation is not granted")
				}
				worktree, err = s.CollaborationWorktree(ctx, w, p, engine)
				if err != nil {
					return reply, err
				}
			}
			if native {
				r.OperationID, err = s.CollaborationFork(ctx, origin, r.TargetID, worktree, p.EventID)
				r.Phase = "fork"
				reply.Status = "preparing"
			} else if worktree != nil {
				r.WorkspaceID = worktree.TargetWorkspaceID
				var lock bool
				if lock, err = s.CollaborationWorktreeLock(ctx, source, w.ID); err == nil {
					r.OperationID, err = s.QueueCollaborationOperation(ctx, CollaborationSession{ID: source.ID, ServerID: w.ServerID, ProjectID: w.ProjectID, WorkspaceID: w.ID}, protocol.OpGitWorktreeCreate, worktree, lock)
				}
				r.Phase = "worktree"
				reply.Status = "preparing"
			} else {
				title := "New session"
				if p.Title != nil {
					title = *p.Title
				}
				err = s.CreateCollaborationSessionTx(ctx, sessiontools.Ref{Engine: engine, ID: r.TargetID}, workspace, title, model, mode, effort)
				if err == nil && strings.TrimSpace(p.Prompt) != "" {
					t, e := s.CollaborationSession(ctx, sessiontools.Ref{Engine: engine, ID: r.TargetID})
					if e != nil {
						return reply, e
					}
					t.Model = model
					t.Mode = mode
					t.Effort = effort
					r.OperationID, err = s.CollaborationSend(ctx, t, p.Prompt)
					reply.Status = "queued"
					if errors.Is(err, s.WorkspaceBusyError()) {
						err = nil
						r.Phase = "send_created"
						reply.Status = "accepted"
						reply.NextAction = "Waiting for the shared workspace. End this turn before waiting, or use directory_mode=worktree or a separate ready workspace to run it in parallel."
					}
				}
			}
			if worktree != nil {
				r.WorkspaceID = worktree.TargetWorkspaceID
			}
			if err != nil {
				if errors.Is(err, s.WorkspaceBusyError()) {
					err = nil
					r.Phase = "await_workspace"
					r.WorkspaceID = workspace
					reply.Status = "accepted"
					reply.NextAction = "Waiting for the shared workspace before forking. End this turn before waiting."
					if worktree != nil {
						reply.NextAction = "Waiting for the source workspace before preparing the new worktree. End this turn before waiting."
					}
				} else {
					return reply, err
				}
			}
		}
		relation := "created"
		if r.Tool == "wio_fork_session" {
			relation = p.ForkMode
		}
		if err = s.LinkCollaboration(ctx, source, grant, root, depth, r, relation, p.AutoContinue); err != nil {
			return reply, err
		}
		reply.Session = &sessiontools.Ref{Engine: r.TargetEngine, ID: r.TargetID}
		reply.OperationID = r.OperationID
		reply.Data = map[string]any{"url": "/?view=" + r.TargetEngine + "&thread=" + r.TargetID, "workspace_id": r.WorkspaceID, "shared_directory": p.DirectoryMode != "worktree", "relation": relation}
		return reply, nil
	}
	target, err := s.AuthorizeCollaboration(ctx, source, grant, p.Session)
	if err != nil {
		return reply, err
	}
	if target.PreparationPhase != "" {
		return reply, sessiontools.Fail("REQUEST_PENDING", "target session is still being prepared")
	}
	r.TargetEngine = target.Engine
	r.TargetID = target.ID
	r.WorkspaceID = target.WorkspaceID
	reply.Session = &p.Session
	if p.Session == source.Ref() {
		return reply, sessiontools.Fail("OUT_OF_SCOPE", "mutations target child or explicitly granted sessions, not the calling turn")
	}
	if r.Tool == "wio_send_message" || r.Tool == "wio_steer_session" {
		if PermissionRank(target.Engine, target.Mode) > PermissionRank(source.Engine, source.Mode) {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "target execution permissions exceed the source session")
		}
	}
	switch r.Tool {
	case "wio_send_message":
		if strings.TrimSpace(p.Prompt) == "" {
			return reply, sessiontools.Fail("INVALID_ARGUMENT", "prompt must not be empty")
		}
		if target.ArchivedAt != nil {
			return reply, s.ActiveError()
		}
		if err = s.CollaborationSendLimit(ctx, source, target.Ref()); err != nil {
			return reply, err
		}
		// Match turn queueing's server -> workspace lock order, including when
		// another browser request starts a turn at the same time.
		if err := s.LockServer(ctx, target.ServerID); err != nil {
			return reply, err
		}
		workspaceBusy, e := s.WorkspaceWriteBusy(ctx, target.WorkspaceID, true)
		if e != nil {
			return reply, e
		}
		// A follow-up supersedes the previous result notification even if the
		// message must wait for the shared workspace before it can be sent.
		if _, err = s.ObserveCollaborationResult(ctx, source, target); err != nil {
			return reply, err
		}
		if (target.Busy() || (s.TurnWritesWorkspace(target.Engine) && workspaceBusy)) && p.Busy == "queue" {
			r.Phase = "send"
			reply.Status = "accepted"
			reply.NextAction = "Message persisted and will be sent after the active turn ends"
			return reply, nil
		}
		r.OperationID, err = s.CollaborationSend(ctx, target, p.Prompt)
		reply.Status = "queued"
	case "wio_steer_session":
		r.OperationID, err = s.CollaborationSteer(ctx, target, p.Prompt, r.ID)
		reply.Status = "queued"
	case "wio_interrupt_session":
		if !target.Busy() {
			return reply, sessiontools.Fail("SESSION_NOT_RUNNING", "session is not running; nothing to interrupt")
		}
		r.OperationID, err = s.Interrupt(ctx, target)
		reply.Status = "queued"
	case "wio_update_session", "wio_archive_session", "wio_restore_session":
		if r.Tool == "wio_archive_session" && target.Busy() {
			return reply, s.ActiveError()
		}
		err = s.Update(ctx, target, r.Tool, p)
	default:
		err = sessiontools.Fail("CAPABILITY_UNSUPPORTED", "unsupported mutation")
	}
	reply.OperationID = r.OperationID
	return reply, err
}
