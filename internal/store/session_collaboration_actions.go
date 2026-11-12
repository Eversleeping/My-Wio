package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/geminiapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type collaborationTargetContext struct{}

func collaborationTargetID(ctx context.Context) string {
	if id, _ := ctx.Value(collaborationTargetContext{}).(string); id != "" {
		return id
	}
	return NewID()
}

func (s *Store) createCollaborationSessionTx(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error {
	now := time.Now().UTC()
	var result sql.Result
	var err error
	switch ref.Engine {
	case "codex":
		result, err = tx.ExecContext(ctx, s.Q("INSERT INTO codex_threads(id,workspace_id,title,created_at,updated_at) SELECT ?,w.id,?,?,? FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL"), ref.ID, title, now, now, workspaceID)
	case "claude":
		result, err = tx.ExecContext(ctx, s.Q(`INSERT INTO claude_threads(id,workspace_id,title,model,permission_mode,created_at,updated_at,model_credential_revision) SELECT ?,w.id,?,?,?,?,?,`+claudeModelRevision+` FROM workspaces w JOIN servers s ON s.id=w.server_id LEFT JOIN server_claude_credential_profiles ccp ON ccp.server_id=s.id LEFT JOIN credential_profiles claude ON claude.id=ccp.profile_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), ref.ID, title, model, mode, now, now, workspaceID)
	case "gemini":
		result, err = tx.ExecContext(ctx, s.Q(`INSERT INTO gemini_threads(id,workspace_id,title,model,approval_mode,thinking_level,created_at,updated_at,model_credential_revision) SELECT ?,w.id,?,?,?,?,?,?,`+geminiCredentialRevision+` FROM workspaces w JOIN servers s ON s.id=w.server_id LEFT JOIN server_gemini_credential_profiles gcp ON gcp.server_id=s.id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), ref.ID, title, model, mode, effort, now, now, workspaceID)
	default:
		return sessiontools.Fail("INVALID_ARGUMENT", "invalid engine")
	}
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
	if ref.Engine == "claude" {
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_options(thread_id,effort) VALUES(?,?)"), ref.ID, effort); err != nil {
			return err
		}
	}
	return s.showProjectForNewThread(ctx, tx, workspaceID)
}

func (s *Store) collaborationClaude(ctx context.Context, tx *sqlx.Tx, id string) (ClaudeThread, error) {
	var t ClaudeThread
	err := tx.GetContext(ctx, &t, s.Q(claudeThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	t.setSteerCapability()
	t.QuestionsSupported = buildinfo.SupportsClaudeQuestions(t.AgentVersion, t.ClaudeVersion)
	return t, err
}
func (s *Store) collaborationGemini(ctx context.Context, tx *sqlx.Tx, id string) (GeminiThread, error) {
	var t GeminiThread
	err := tx.GetContext(ctx, &t, s.Q(geminiThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	t.SteerSupported = buildinfo.SupportsGeminiSteering(t.AgentVersion, t.CLIVersion)
	return t, err
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
	if strings.HasPrefix(kind, "codex.") {
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

func (s *Store) queueCodexTurnTx(ctx context.Context, tx *sqlx.Tx, t Thread, command protocol.StartTurnCommand) (string, protocol.StreamEvent, error) {
	result, err := tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET status='queued',updated_at=? WHERE id=? AND archived_at IS NULL AND status NOT IN ('queued','running')"), time.Now().UTC(), t.ID)
	if err != nil {
		return "", protocol.StreamEvent{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return "", protocol.StreamEvent{}, ErrThreadActive
	}
	var native string
	if err = tx.GetContext(ctx, &native, s.Q("SELECT codex_thread_id FROM codex_threads WHERE id=?"), t.ID); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	command.CodexThread = native
	op, err := s.queueCollaborationOperation(ctx, tx, CollaborationSession{ID: t.ID, ServerID: t.ServerID, ProjectID: t.ProjectID, WorkspaceID: t.WorkspaceID}, "codex.turn.start", command, false)
	if err != nil {
		return "", protocol.StreamEvent{}, err
	}
	raw, _ := json.Marshal(map[string]any{"text": command.Prompt, "image_count": len(command.Images), "operation_id": op})
	event := protocol.StreamEvent{EventID: NewID(), StreamID: t.ID, Kind: "user.message", Payload: raw, OccurredAt: time.Now().UTC()}
	if s.isPostgres() {
		if err = lockEventStream(ctx, tx, t.ID); err != nil {
			return "", event, err
		}
	}
	if err = tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), t.ID); err != nil {
		return "", event, err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(raw))
	return op, event, err
}

func (s *Store) collaborationSend(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", sessiontools.Fail("INVALID_ARGUMENT", "prompt must not be empty")
	}
	if t.ArchivedAt != nil || t.Busy() {
		return "", ErrThreadActive
	}
	switch t.Engine {
	case "codex":
		op, _, err := s.queueCodexTurnTx(ctx, tx, Thread{ID: t.ID, ServerID: t.ServerID, ProjectID: t.ProjectID, WorkspaceID: t.WorkspaceID}, protocol.StartTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID, WorkspaceID: t.WorkspaceID, Workspace: t.Path, Prompt: prompt, Model: t.Model, ReasoningEffort: t.Effort, ApprovalMode: t.Mode})
		return op, err
	case "claude":
		native, err := s.collaborationClaude(ctx, tx, t.ID)
		if err != nil {
			return "", err
		}
		_, err = s.queueClaudeInputTx(ctx, tx, native, "", prompt, t.Model, t.Mode, t.Effort, nil)
		if err != nil {
			return "", err
		}
	case "gemini":
		native, err := s.collaborationGemini(ctx, tx, t.ID)
		if err != nil {
			return "", err
		}
		_, err = s.queueGeminiTurnTx(ctx, tx, native, prompt, t.Model, t.Mode, "", nil, nil, nil, t.Effort)
		if err != nil {
			return "", err
		}
	}
	var op string
	err := tx.GetContext(ctx, &op, s.Q("SELECT operation_id FROM "+t.Engine+"_threads WHERE id=?"), t.ID)
	return op, err
}

// collaborationModelOverride applies the caller's optional model and effort.
// An explicit model clears the inherited effort unless effort is also given,
// because effort levels are not portable between models.
func collaborationModelOverride(engine, model, effort string, p sessiontools.Params) (string, string, error) {
	if p.Model != "" {
		if !collaborationModelPattern.MatchString(strings.TrimSuffix(p.Model, "[1m]")) || len(p.Model) > 128 || engine != "claude" && strings.HasSuffix(p.Model, "[1m]") {
			return "", "", sessiontools.Fail("INVALID_ARGUMENT", "invalid model")
		}
		if p.Model != model {
			model, effort = p.Model, ""
		}
	}
	if p.Effort != "" {
		effort = p.Effort
	}
	valid := true
	switch engine {
	case "codex":
		switch effort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		default:
			valid = false
		}
	case "claude":
		valid = protocol.ValidClaudeEffort(effort)
	case "gemini":
		valid = geminiapi.ValidThinkingLevel(model, effort)
	}
	if !valid {
		if p.Effort == "" {
			// Only an explicit request can fail; a default that no longer fits is dropped.
			return model, "", nil
		}
		return "", "", sessiontools.Fail("INVALID_ARGUMENT", "effort "+strconv.Quote(p.Effort)+" is not supported by "+engine+" model "+strconv.Quote(model))
	}
	return model, effort, nil
}

var collaborationModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func collaborationPermissionRank(engine, mode string) int {
	if mode == "plan" {
		return 0
	}
	if mode == "never" || mode == "yolo" || mode == "bypassPermissions" {
		return 3
	}
	if mode == "acceptEdits" || mode == "auto_edit" {
		return 2
	}
	return 1
}

func (s *Store) collaborationDefaults(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, grant sessiontools.Grant, engine, workspace string) (string, string, string, error) {
	if engine == source.Engine {
		return source.Model, source.Mode, source.Effort, nil
	}
	mode := grant.TargetModes[engine]
	if !validCollaborationMode(engine, mode) {
		return "", "", "", sessiontools.Fail("PERMISSION_MAPPING_REQUIRED", "choose the target engine's permission mode in session collaboration settings")
	}
	if collaborationPermissionRank(engine, mode) > collaborationPermissionRank(source.Engine, source.Mode) {
		return "", "", "", sessiontools.Fail("OUT_OF_SCOPE", "target execution permissions would exceed the source session")
	}
	model := ""
	switch engine {
	case "claude":
		model = "sonnet"
		var configured string
		err := tx.GetContext(ctx, &configured, s.Q("SELECT COALESCE(NULLIF(b.model,''),p.model,'') FROM workspaces w LEFT JOIN server_claude_credential_profiles b ON b.server_id=w.server_id LEFT JOIN credential_profiles p ON p.id=b.profile_id WHERE w.id=?"), workspace)
		if err != nil {
			return "", "", "", err
		}
		if configured != "" {
			model = configured
		}
	case "gemini":
		model = "auto"
	}
	return model, mode, "", nil
}

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
	reply := sessiontools.Reply{RequestID: p.RequestID, Status: "succeeded"}
	if r.Tool == "wio_create_session" || r.Tool == "wio_fork_session" {
		depth, err := s.collaborationLimits(ctx, tx, source, root)
		if err != nil {
			return reply, err
		}
		if p.AutoContinue && !grant.AutoContinue {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "automatic continuation is not granted")
		}
		origin := source
		if r.Tool == "wio_fork_session" {
			origin, err = s.authorizeCollaboration(ctx, tx, source, grant, p.Session)
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
		if allowed, e := s.collaborationWorkspaceGranted(ctx, tx, source, grant, workspace); e != nil {
			return reply, e
		} else if !allowed {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "workspace is not granted")
		}
		var w Workspace
		if err = tx.GetContext(ctx, &w, s.Q("SELECT * FROM workspaces WHERE id=? AND project_id=? AND status='ready'"), workspace, source.ProjectID); err != nil {
			return reply, err
		}
		model, mode, effort, err := s.collaborationDefaults(ctx, tx, source, grant, engine, workspace)
		if err != nil {
			return reply, err
		}
		execution, _ := json.Marshal(map[string]string{"model": model, "mode": mode, "effort": effort})
		r.Execution = string(execution)
		var version string
		if err = tx.GetContext(ctx, &version, s.Q("SELECT agent_version FROM servers WHERE id=? AND revoked_at IS NULL"), w.ServerID); err != nil {
			return reply, err
		}
		if !buildinfo.SupportsSessionCollaboration(version) {
			return reply, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "update the target Agent for Wio collaboration")
		}
		r.TargetEngine = engine
		r.TargetID = NewID()
		r.WorkspaceID = workspace
		native := r.Tool == "wio_fork_session" && p.ForkMode != "handoff"
		if native && collaborationPermissionRank(origin.Engine, origin.Mode) > collaborationPermissionRank(source.Engine, source.Mode) {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "native source execution permissions exceed the calling session")
		}
		if native && (engine != origin.Engine || workspace != origin.WorkspaceID) {
			return reply, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "native forks require the same engine and source workspace; choose handoff otherwise")
		}
		if native {
			model, mode, effort = origin.Model, origin.Mode, origin.Effort
		}
		if model, effort, err = collaborationModelOverride(engine, model, effort, p); err != nil {
			return reply, err
		}
		execution, _ = json.Marshal(map[string]string{"model": model, "mode": mode, "effort": effort})
		r.Execution = string(execution)
		if r.Tool == "wio_fork_session" && p.ForkMode == "handoff" {
			if strings.TrimSpace(p.Handoff) == "" {
				return reply, sessiontools.Fail("INVALID_ARGUMENT", "handoff requires explicit context")
			}
			p.Prompt = handoffPrompt(p)
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
				worktree, err = s.collaborationWorktree(ctx, tx, w, p, engine)
				if err != nil {
					return reply, err
				}
			}
			if native {
				r.OperationID, err = s.collaborationFork(ctx, tx, origin, r.TargetID, worktree, p.EventID)
				r.Phase = "fork"
				reply.Status = "preparing"
			} else if worktree != nil {
				r.WorkspaceID = worktree.TargetWorkspaceID
				var lock bool
				if lock, err = s.collaborationWorktreeLock(ctx, tx, source, w.ID); err == nil {
					r.OperationID, err = s.queueCollaborationOperation(ctx, tx, CollaborationSession{ID: source.ID, ServerID: w.ServerID, ProjectID: w.ProjectID, WorkspaceID: w.ID}, "git.worktree.create", worktree, lock)
				}
				r.Phase = "worktree"
				reply.Status = "preparing"
			} else {
				title := "New session"
				if p.Title != nil {
					title = *p.Title
				}
				err = s.createCollaborationSessionTx(ctx, tx, sessiontools.Ref{Engine: engine, ID: r.TargetID}, workspace, title, model, mode, effort)
				if err == nil && strings.TrimSpace(p.Prompt) != "" {
					t, e := s.collaborationSession(ctx, tx, sessiontools.Ref{Engine: engine, ID: r.TargetID})
					if e != nil {
						return reply, e
					}
					t.Model = model
					t.Mode = mode
					t.Effort = effort
					r.OperationID, err = s.collaborationSend(ctx, tx, t, p.Prompt)
					reply.Status = "queued"
					if errors.Is(err, ErrWorkspaceWriteActive) {
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
				if errors.Is(err, ErrWorkspaceWriteActive) {
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
		if err = s.linkCollaboration(ctx, tx, source, grant, root, depth, r, relation, p.AutoContinue); err != nil {
			return reply, err
		}
		reply.Session = &sessiontools.Ref{Engine: r.TargetEngine, ID: r.TargetID}
		reply.OperationID = r.OperationID
		reply.Data = map[string]any{"url": "/?view=" + r.TargetEngine + "&thread=" + r.TargetID, "workspace_id": r.WorkspaceID, "shared_directory": p.DirectoryMode != "worktree", "relation": relation}
		return reply, nil
	}
	target, err := s.authorizeCollaboration(ctx, tx, source, grant, p.Session)
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
		if collaborationPermissionRank(target.Engine, target.Mode) > collaborationPermissionRank(source.Engine, source.Mode) {
			return reply, sessiontools.Fail("OUT_OF_SCOPE", "target execution permissions exceed the source session")
		}
	}
	switch r.Tool {
	case "wio_send_message":
		if strings.TrimSpace(p.Prompt) == "" {
			return reply, sessiontools.Fail("INVALID_ARGUMENT", "prompt must not be empty")
		}
		if target.ArchivedAt != nil {
			return reply, ErrThreadActive
		}
		if err = s.collaborationSendLimit(ctx, tx, source, target.Ref()); err != nil {
			return reply, err
		}
		// Match turn queueing's server -> workspace lock order, including when
		// another browser request starts a turn at the same time.
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), target.ServerID); err != nil {
			return reply, err
		}
		workspaceBusy, e := s.workspaceWriteBusy(ctx, tx, target.WorkspaceID, true)
		if e != nil {
			return reply, e
		}
		if (target.Busy() || (target.Engine != "codex" && workspaceBusy)) && p.Busy == "queue" {
			r.Phase = "send"
			reply.Status = "accepted"
			reply.NextAction = "Message persisted and will be sent after the active turn ends"
			return reply, nil
		}
		r.OperationID, err = s.collaborationSend(ctx, tx, target, p.Prompt)
		reply.Status = "queued"
	case "wio_steer_session":
		r.OperationID, err = s.collaborationSteer(ctx, tx, target, p.Prompt, r.ID)
		reply.Status = "queued"
	case "wio_interrupt_session":
		if !target.Busy() {
			return reply, sessiontools.Fail("SESSION_NOT_RUNNING", "session is not running; nothing to interrupt")
		}
		switch target.Engine {
		case "claude":
			var t ClaudeThread
			t, err = s.collaborationClaude(ctx, tx, target.ID)
			if err == nil {
				r.OperationID, err = s.interruptClaudeTurnTx(ctx, tx, t)
			}
		case "gemini":
			var t GeminiThread
			t, err = s.collaborationGemini(ctx, tx, target.ID)
			if err == nil {
				r.OperationID, err = s.interruptGeminiTurnTx(ctx, tx, t)
			}
		case "codex":
			r.OperationID, err = s.collaborationCodexControl(ctx, tx, target, "", true)
		}
		reply.Status = "queued"
	case "wio_update_session", "wio_archive_session", "wio_restore_session":
		if r.Tool == "wio_archive_session" && target.Busy() {
			return reply, ErrThreadActive
		}
		sets := []string{"updated_at=?"}
		args := []any{time.Now().UTC()}
		table := target.Engine + "_threads"
		if r.Tool == "wio_archive_session" {
			sets = append(sets, "archived_at=?")
			args = append(args, time.Now().UTC())
		}
		if r.Tool == "wio_restore_session" {
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
			if target.Engine == "claude" {
				_, err = tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_management(thread_id,pinned_at) VALUES(?,?) ON CONFLICT(thread_id) DO UPDATE SET pinned_at=excluded.pinned_at"), target.ID, at)
			} else {
				sets = append(sets, "pinned_at=?")
				args = append(args, at)
			}
		}
		if err == nil {
			args = append(args, target.ID)
			_, err = tx.ExecContext(ctx, s.Q("UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE id=?"), args...)
		}
	default:
		err = sessiontools.Fail("CAPABILITY_UNSUPPORTED", "unsupported mutation")
	}
	reply.OperationID = r.OperationID
	return reply, err
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

func (s *Store) collaborationFork(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error) {
	if source.Engine != "codex" || worktree != nil {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), source.ServerID); err != nil {
			return "", err
		}
		busy, err := s.collaborationWorkspaceBusy(ctx, tx, source.WorkspaceID)
		if err != nil {
			return "", err
		}
		if busy {
			return "", ErrWorkspaceWriteActive
		}
	}
	ctx = context.WithValue(ctx, collaborationTargetContext{}, target)
	switch source.Engine {
	case "claude":
		t, err := s.collaborationClaude(ctx, tx, source.ID)
		if err != nil {
			return "", err
		}
		op, _, err := s.queueClaudeForkTx(ctx, tx, t, worktree, eventID)
		return op, err
	case "gemini":
		t, err := s.collaborationGemini(ctx, tx, source.ID)
		if err != nil {
			return "", err
		}
		op, _, err := s.queueGeminiForkTx(ctx, tx, t, eventID, worktree)
		return op, err
	case "codex":
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
			return s.queueCollaborationOperation(ctx, tx, source, "git.worktree.create", worktree, true)
		}
		command := protocol.ForkThreadCommand{SourceThreadID: source.ID, TargetThreadID: target, CodexThread: source.NativeID, WorkspaceID: source.WorkspaceID, Workspace: source.Path, Title: source.Title + " (continued)"}
		if eventID != "" {
			var err error
			command.LastTurnID, command.CutoffSequence, err = s.threadForkBoundary(ctx, tx, source.ID, eventID)
			if err != nil {
				return "", err
			}
		}
		return s.queueCollaborationOperation(ctx, tx, source, "codex.thread.fork", command, false)
	}
	return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "native fork is unavailable")
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

func (s *Store) collaborationCodexControl(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt string, interrupt bool) (string, error) {
	if interrupt && t.OperationID != "" {
		result, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='cancelled',result='cancelled before Codex turn started',completed_at=? WHERE id=? AND status='queued'"), time.Now().UTC(), t.OperationID)
		if err != nil {
			return "", err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			if _, err = tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET status='idle',updated_at=? WHERE id=?"), time.Now().UTC(), t.ID); err != nil {
				return "", err
			}
			if _, err = tx.ExecContext(ctx, s.Q("UPDATE codex_scheduled_tasks SET last_run_status='cancelled',last_run_message='cancelled before Codex turn started',updated_at=? WHERE last_operation_id=?"), time.Now().UTC(), t.OperationID); err != nil {
				return "", err
			}
			return t.OperationID, s.addCollaborationEvent(ctx, tx, t.ID, "codex.turn.cancelled", map[string]string{"operation_id": t.OperationID, "source": "control"})
		}
		// Delivered turns can be stopped before the native accepted event arrives.
		if t.Status == "queued" {
			return s.queueCollaborationOperation(ctx, tx, t, "codex.turn.interrupt", protocol.InterruptTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID}, false)
		}
	}
	if t.Status != "running" || t.NativeID == "" {
		return "", ErrThreadActive
	}
	var payload string
	if err := tx.GetContext(ctx, &payload, s.Q("SELECT payload FROM events WHERE stream_id=? AND kind IN ('turn.accepted','codex.turn.started','codex.turn.completed','codex.turn.failed','codex.turn.cancelled') ORDER BY sequence DESC LIMIT 1"), t.ID); err != nil {
		return "", err
	}
	var value struct {
		TurnID string `json:"turn_id"`
		Turn   struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal([]byte(payload), &value) != nil {
		return "", ErrThreadActive
	}
	turn := value.TurnID
	if turn == "" && value.Turn.Status != "completed" && value.Turn.Status != "failed" && value.Turn.Status != "interrupted" {
		turn = value.Turn.ID
	}
	if turn == "" {
		return "", ErrThreadActive
	}
	if interrupt {
		return s.queueCollaborationOperation(ctx, tx, t, "codex.turn.interrupt", protocol.InterruptTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID, TurnID: turn}, false)
	}
	return s.queueCollaborationOperation(ctx, tx, t, "codex.turn.steer", protocol.SteerTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID, ExpectedTurnID: turn, Prompt: prompt}, false)
}

func (s *Store) collaborationSteer(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt, key string) (string, error) {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 64<<10 {
		return "", sessiontools.Fail("INVALID_ARGUMENT", "steering requires a prompt of at most 64 KiB")
	}
	switch t.Engine {
	case "codex":
		return s.collaborationCodexControl(ctx, tx, t, prompt, false)
	case "claude":
		native, err := s.collaborationClaude(ctx, tx, t.ID)
		if err != nil {
			return "", err
		}
		if !native.SteerSupported {
			return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "Claude steering is unavailable")
		}
		_, op, err := s.queueClaudeSteerTx(ctx, tx, protocol.ClaudeSteerCommand{ThreadID: t.ID, OperationID: t.OperationID, RequestID: key, Prompt: prompt})
		return op, err
	case "gemini":
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
	return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "steering is unavailable")
}
