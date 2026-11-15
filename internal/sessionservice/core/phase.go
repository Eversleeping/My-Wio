package core

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type PhasePort interface {
	Port
	Operation(context.Context, string) (Operation, error)
	ValidMode(string, string) bool
	SetExecution(context.Context, sessiontools.Ref, string, string, string) error
	DeferredExecution(string) bool
}

func Advance(s PhasePort, ctx context.Context, source CollaborationSession, grant sessiontools.Grant, r *CollaborationRequest, p sessiontools.Params, reply *sessiontools.Reply) error {
	ref := sessiontools.Ref{Engine: r.TargetEngine, ID: r.TargetID}
	switch r.Phase {
	case "await_source", "await_workspace":
		if r.Tool == "wio_create_session" || p.ForkMode == "handoff" {
			w, err := s.ReadyWorkspace(ctx, r.WorkspaceID, "")
			if err != nil {
				return err
			}
			if !grant.Worktree || !WorkspaceAllowed(source, grant, w.ID, w.ProjectID) {
				return sessiontools.Fail("OUT_OF_SCOPE", "worktree creation is no longer granted")
			}
			command, err := s.CollaborationWorktree(ctx, w, p, r.TargetEngine)
			if err != nil {
				return err
			}
			lock, err := s.CollaborationWorktreeLock(ctx, source, w.ID)
			if err != nil {
				return err
			}
			r.OperationID, err = s.QueueCollaborationOperation(ctx, CollaborationSession{ID: source.ID, ServerID: w.ServerID, ProjectID: w.ProjectID, WorkspaceID: w.ID}, protocol.OpGitWorktreeCreate, command, lock)
			if err != nil {
				return err
			}
			r.WorkspaceID = command.TargetWorkspaceID
			r.Phase = "worktree"
			reply.Status = "preparing"
			reply.OperationID = r.OperationID
			return nil
		}
		origin, err := s.AuthorizeCollaboration(ctx, source, grant, p.Session)
		if err != nil {
			return err
		}
		if origin.Busy() {
			return s.ActiveError()
		}
		if origin.ArchivedAt != nil {
			return sessiontools.Fail("OUT_OF_SCOPE", "source fork session was archived")
		}
		if PermissionRank(origin.Engine, origin.Mode) > PermissionRank(source.Engine, source.Mode) {
			return sessiontools.Fail("OUT_OF_SCOPE", "native source execution permissions changed during preparation")
		}
		w, e := s.ReadyWorkspace(ctx, origin.WorkspaceID, "")
		if err = e; err != nil {
			return err
		}
		command, err := s.CollaborationWorktree(ctx, w, p, r.TargetEngine)
		if err != nil {
			return err
		}
		if p.DirectoryMode != "worktree" {
			command = nil
		} else if !grant.Worktree {
			return sessiontools.Fail("OUT_OF_SCOPE", "worktree creation is not granted")
		}
		r.OperationID, err = s.CollaborationFork(ctx, origin, r.TargetID, command, p.EventID)
		if err != nil {
			return err
		}
		r.Phase = "fork"
		if command != nil {
			r.WorkspaceID = command.TargetWorkspaceID
		}
		reply.Status = "preparing"
		reply.OperationID = r.OperationID
		reply.NextAction = "Poll wio_get_request for fork preparation"
		return nil
	case "worktree", "fork":
		op, err := s.Operation(ctx, r.OperationID)
		if err != nil {
			return err
		}
		if op.Status == "queued" || op.Status == "delivered" || op.Status == "running" {
			return s.ActiveError()
		}
		if op.Status != "succeeded" {
			return sessiontools.Fail("PREPARATION_FAILED", op.Result)
		}
		var execution struct {
			Model  string `json:"model"`
			Mode   string `json:"mode"`
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal([]byte(r.Execution), &execution); err != nil {
			return err
		}
		if !s.ValidMode(r.TargetEngine, execution.Mode) || PermissionRank(r.TargetEngine, execution.Mode) > PermissionRank(source.Engine, source.Mode) {
			return sessiontools.Fail("OUT_OF_SCOPE", "source execution permissions changed during preparation")
		}
		if r.Phase == "fork" {
			if err := s.SetExecution(ctx, ref, execution.Model, execution.Mode, execution.Effort); err != nil {
				return err
			}
		}
		if r.Phase == "worktree" {
			title := "New session"
			if p.Title != nil {
				title = *p.Title
			}
			if err := s.CreateCollaborationSessionTx(ctx, ref, r.WorkspaceID, title, execution.Model, execution.Mode, execution.Effort); err != nil {
				return err
			}
		}
		r.Phase = "send_created"
		fallthrough
	case "send_created", "send":
		t, err := s.CollaborationSession(ctx, ref)
		if err != nil {
			return err
		}
		if t.ArchivedAt != nil {
			return sessiontools.Fail("OUT_OF_SCOPE", "target session was archived")
		}
		if err = s.CollaborationSendLimit(ctx, source, ref); err != nil {
			return err
		}
		prompt := HandoffPrompt(p)
		if strings.TrimSpace(prompt) == "" {
			reply.Status = "succeeded"
			r.Phase = ""
			return nil
		}
		if s.DeferredExecution(t.Engine) && r.Phase != "send" {
			var execution struct {
				Model  string `json:"model"`
				Mode   string `json:"mode"`
				Effort string `json:"effort"`
			}
			if err = json.Unmarshal([]byte(r.Execution), &execution); err != nil {
				return err
			}
			t.Model, t.Mode, t.Effort = execution.Model, execution.Mode, execution.Effort
		}
		if PermissionRank(t.Engine, t.Mode) > PermissionRank(source.Engine, source.Mode) {
			return sessiontools.Fail("OUT_OF_SCOPE", "target execution permissions exceed the source session")
		}
		if r.Phase == "send" {
			if _, err = s.AuthorizeCollaboration(ctx, source, grant, ref); err != nil {
				return err
			}
		}
		r.OperationID, err = s.CollaborationSend(ctx, t, prompt)
		if err != nil {
			return err
		}
		r.Phase = ""
		reply.OperationID = r.OperationID
		reply.Status = "queued"
		reply.NextAction = "Read the target session or wait for its turn result"
		return nil
	}
	return sessiontools.Fail("INTERNAL_ERROR", "unknown preparation phase")
}
