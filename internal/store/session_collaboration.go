package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type CollaborationSession struct {
	Engine           string     `db:"engine" json:"engine"`
	ID               string     `db:"id" json:"id"`
	Title            string     `db:"title" json:"title"`
	Status           string     `db:"status" json:"status"`
	WorkspaceID      string     `db:"workspace_id" json:"workspace_id"`
	ProjectID        string     `db:"project_id" json:"project_id"`
	ServerID         string     `db:"server_id" json:"server_id"`
	Path             string     `db:"path" json:"path"`
	Model            string     `db:"model" json:"model"`
	Mode             string     `db:"mode" json:"permission_mode"`
	Effort           string     `db:"effort" json:"effort"`
	NativeID         string     `db:"native_id" json:"-"`
	OperationID      string     `db:"operation_id" json:"operation_id"`
	AgentVersion     string     `db:"agent_version" json:"-"`
	ArchivedAt       *time.Time `db:"archived_at" json:"archived_at"`
	UpdatedAt        time.Time  `db:"updated_at" json:"updated_at"`
	URL              string     `db:"-" json:"url"`
	RecoveryPending  bool       `db:"-" json:"recovery_pending"`
	PreparationPhase string     `db:"-" json:"preparation_phase,omitempty"`
	WaitingUser      bool       `db:"-" json:"waiting_user"`
}

func (t CollaborationSession) Ref() sessiontools.Ref {
	return sessiontools.Ref{Engine: t.Engine, ID: t.ID}
}
func (t CollaborationSession) Busy() bool {
	return t.Status == "queued" || t.Status == "running" || t.Status == "interrupting" || t.Status == "preparing" || t.RecoveryPending
}

const collaborationSessions = `SELECT 'codex' AS engine,t.id,t.title,t.status,t.workspace_id,w.project_id,w.server_id,w.path,'' AS model,'on-request' AS mode,'' AS effort,t.codex_thread_id AS native_id,COALESCE((SELECT o.id FROM agent_operations o WHERE o.thread_id=t.id AND o.kind IN ('codex.turn.start','codex.turn.rewrite') ORDER BY o.created_at DESC LIMIT 1),'') AS operation_id,s.agent_version,t.archived_at,t.updated_at FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id WHERE s.revoked_at IS NULL
 UNION ALL SELECT 'claude',t.id,t.title,t.status,t.workspace_id,w.project_id,w.server_id,w.path,t.model,t.permission_mode,COALESCE(o.effort,''),t.session_id,t.operation_id,s.agent_version,t.archived_at,t.updated_at FROM claude_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id LEFT JOIN claude_thread_options o ON o.thread_id=t.id WHERE s.revoked_at IS NULL
 UNION ALL SELECT 'gemini',t.id,t.title,t.status,t.workspace_id,w.project_id,w.server_id,w.path,t.model,t.approval_mode,t.thinking_level,t.session_id,t.operation_id,s.agent_version,t.archived_at,t.updated_at FROM gemini_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id WHERE s.revoked_at IS NULL`

func (s *Store) collaborationSession(ctx context.Context, db sqlx.QueryerContext, ref sessiontools.Ref) (CollaborationSession, error) {
	var t CollaborationSession
	if !ref.Valid() {
		return t, sessiontools.Fail("INVALID_ARGUMENT", "engine and Wio session id are required")
	}
	err := sqlx.GetContext(ctx, db, &t, s.Q("SELECT * FROM ("+collaborationSessions+") sessions WHERE engine=? AND id=?"), ref.Engine, ref.ID)
	if err != nil {
		return t, err
	}
	t.URL = "/?view=" + t.Engine + "&thread=" + t.ID
	if t.Engine == "codex" && t.OperationID == "" {
		var execution string
		if e := sqlx.GetContext(ctx, db, &execution, s.Q("SELECT execution_json FROM session_tool_requests WHERE target_engine='codex' AND target_id=? AND tool IN ('wio_create_session','wio_fork_session') ORDER BY created_at LIMIT 1"), t.ID); e == nil {
			var options struct {
				Model  string `json:"model"`
				Mode   string `json:"mode"`
				Effort string `json:"effort"`
			}
			if json.Unmarshal([]byte(execution), &options) == nil && options.Mode != "" {
				t.Model, t.Mode, t.Effort = options.Model, options.Mode, options.Effort
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return t, e
		}
	}
	if t.OperationID != "" {
		var op struct {
			Status  string `db:"status"`
			Payload string `db:"payload"`
		}
		if err = sqlx.GetContext(ctx, db, &op, s.Q("SELECT status,payload FROM agent_operations WHERE id=?"), t.OperationID); err != nil {
			return t, err
		}
		t.RecoveryPending = t.Status == "interrupted" && (op.Status == "queued" || op.Status == "delivered" || op.Status == "running")
		if t.Engine == "codex" {
			var p protocol.StartTurnCommand
			var rewrite protocol.RewriteTurnCommand
			_ = json.Unmarshal([]byte(op.Payload), &p)
			if p.ThreadID == "" {
				_ = json.Unmarshal([]byte(op.Payload), &rewrite)
				p = rewrite.Start
			}
			t.Model = p.Model
			t.Effort = p.ReasoningEffort
			if p.ApprovalMode != "" {
				t.Mode = p.ApprovalMode
			}
		}
	}
	return t, nil
}

func (s *Store) CollaborationSession(ctx context.Context, ref sessiontools.Ref) (CollaborationSession, error) {
	t, err := s.collaborationSession(ctx, s.DB, ref)
	if err != nil {
		return t, err
	}
	err = s.decorateCollaborationSession(ctx, s.DB, &t)
	return t, err
}

func (s *Store) decorateCollaborationSession(ctx context.Context, db sqlx.QueryerContext, t *CollaborationSession) error {
	var phase string
	err := sqlx.GetContext(ctx, db, &phase, s.Q("SELECT phase FROM session_tool_requests WHERE target_engine=? AND target_id=? AND phase<>'' AND status IN ('accepted','preparing') ORDER BY created_at LIMIT 1"), t.Engine, t.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		t.PreparationPhase = phase
		if !t.Busy() {
			t.Status = "preparing"
		}
	}
	t.WaitingUser, err = s.collaborationWaitingUser(ctx, db, *t)
	return err
}

func (s *Store) collaborationGrant(ctx context.Context, db sqlx.QueryerContext, key string) (sessiontools.Grant, int, error) {
	var row struct {
		JSON          string `db:"grant_json"`
		Version       int    `db:"version"`
		Parent        string `db:"parent_key"`
		ParentVersion int    `db:"parent_version"`
	}
	err := sqlx.GetContext(ctx, db, &row, s.Q("SELECT grant_json,version,parent_key,parent_version FROM session_tool_grants WHERE source_key=?"), key)
	if errors.Is(err, sql.ErrNoRows) {
		return sessiontools.Grant{}, 0, nil
	}
	if err != nil {
		return sessiontools.Grant{}, 0, err
	}
	var grant sessiontools.Grant
	err = json.Unmarshal([]byte(row.JSON), &grant)
	if err == nil && row.Parent != "" {
		parent, parentVersion, parentErr := s.collaborationGrant(ctx, db, row.Parent)
		if parentErr != nil {
			return grant, row.Version, parentErr
		}
		if !parent.Enabled || !parent.Delegate || parentVersion != row.ParentVersion {
			grant.Enabled = false
		}
	}

	return grant, row.Version, err
}

func (s *Store) CollaborationGrant(ctx context.Context, ref sessiontools.Ref) (sessiontools.Grant, error) {
	g, _, err := s.collaborationGrant(ctx, s.DB, ref.Key())
	return g, err
}

func validCollaborationMode(engine, mode string) bool {
	switch engine {
	case "codex":
		return mode == "on-request" || mode == "untrusted" || mode == "never"
	case "claude":
		return mode == "default" || mode == "acceptEdits" || mode == "plan" || mode == "bypassPermissions"
	case "gemini":
		return mode == "default" || mode == "auto_edit" || mode == "plan" || mode == "yolo"
	}
	return false
}

// SetCollaborationGrant is only exposed through the authenticated browser API.
// The version is checked again when deferred work or auto-continuations execute.
func (s *Store) SetCollaborationGrant(ctx context.Context, ref sessiontools.Ref, grant sessiontools.Grant) error {
	source, err := s.CollaborationSession(ctx, ref)
	if err != nil {
		return err
	}
	if len(grant.WorkspaceIDs) > 50 || len(grant.SessionRefs) > 50 {
		return sessiontools.Fail("INVALID_ARGUMENT", "too many granted resources")
	}
	for _, id := range grant.WorkspaceIDs {
		w, e := s.Workspace(ctx, id)
		if e != nil || w.ProjectID != source.ProjectID {
			return sessiontools.Fail("OUT_OF_SCOPE", "workspace must belong to this project")
		}
	}
	for _, r := range grant.SessionRefs {
		t, e := s.CollaborationSession(ctx, r)
		if e != nil || t.ProjectID != source.ProjectID {
			return sessiontools.Fail("OUT_OF_SCOPE", "session must belong to this project")
		}
	}
	for engine, mode := range grant.TargetModes {
		if !validCollaborationMode(engine, mode) {
			return sessiontools.Fail("INVALID_ARGUMENT", "invalid target permission mode")
		}
	}
	raw, _ := json.Marshal(grant)
	_, err = s.DB.ExecContext(ctx, s.Q(`INSERT INTO session_tool_grants(source_key,grant_json,version,updated_at) VALUES(?,?,1,?) ON CONFLICT(source_key) DO UPDATE SET grant_json=excluded.grant_json,parent_key='',parent_version=0,version=session_tool_grants.version+1,updated_at=excluded.updated_at`), ref.Key(), string(raw), time.Now().UTC())
	return err
}

type CollaborationRequest struct {
	ID           string              `db:"id" json:"id"`
	SourceKey    string              `db:"source_key" json:"source_key"`
	RequestKey   string              `db:"request_key" json:"request_id"`
	Tool         string              `db:"tool" json:"tool"`
	Arguments    string              `db:"arguments" json:"-"`
	Execution    string              `db:"execution_json" json:"-"`
	GrantVersion int                 `db:"grant_version" json:"-"`
	Status       string              `db:"status" json:"status"`
	Phase        string              `db:"phase" json:"phase"`
	TargetEngine string              `db:"target_engine" json:"target_engine"`
	TargetID     string              `db:"target_id" json:"target_id"`
	WorkspaceID  string              `db:"workspace_id" json:"workspace_id"`
	OperationID  string              `db:"operation_id" json:"operation_id"`
	Result       string              `db:"result" json:"-"`
	CreatedAt    time.Time           `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time           `db:"updated_at" json:"updated_at"`
	Error        *sessiontools.Error `db:"-" json:"error,omitempty"`
}

func decodeCollaborationParams(call sessiontools.Call) (sessiontools.Params, error) {
	var p sessiontools.Params
	var fields map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &fields) != nil || fields == nil {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "arguments must be an object")
	}
	var definition *sessiontools.Tool
	for _, d := range sessiontools.Definitions() {
		if d.Name == call.Name {
			copy := d
			definition = &copy
			break
		}
	}
	if definition == nil {
		return p, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "unknown Wio tool")
	}
	properties := definition.InputSchema["properties"].(map[string]any)
	for key, value := range fields {
		if properties[key] == nil || string(value) == "null" {
			return p, sessiontools.Fail("INVALID_ARGUMENT", "unexpected or null argument: "+key)
		}
	}
	for _, key := range definition.InputSchema["required"].([]string) {
		if fields[key] == nil {
			return p, sessiontools.Fail("INVALID_ARGUMENT", "missing argument: "+key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "invalid argument types")
	}
	if fields["session"] != nil && !p.Session.Valid() {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "invalid session identity")
	}
	for _, ref := range p.Sessions {
		if !ref.Valid() {
			return p, sessiontools.Fail("INVALID_ARGUMENT", "invalid session identity")
		}
	}
	if len(p.RequestID) > 160 || len(p.Prompt) > 256<<10 || len(p.Handoff) > 128<<10 || p.After < 0 || p.Offset < 0 || p.Offset > 100000 || p.Limit < 0 || p.Limit > 100 || p.TimeoutMS < 0 || p.TimeoutMS > 30000 || len(p.Sessions) > 8 || len(p.Branch) > 200 || len(p.BaseRef) > 240 {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "argument exceeds limits")
	}
	if sessiontools.Mutating(call.Name) && strings.TrimSpace(p.RequestID) == "" {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "stable request_id is required")
	}
	if p.DirectoryMode != "" && p.DirectoryMode != "shared" && p.DirectoryMode != "worktree" || p.Busy != "" && p.Busy != "reject" && p.Busy != "queue" || p.ForkMode != "" && p.ForkMode != "native" && p.ForkMode != "handoff" && p.ForkMode != "after_turn" {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "invalid execution mode")
	}
	if p.Engine != "" && p.Engine != "codex" && p.Engine != "claude" && p.Engine != "gemini" {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "invalid engine")
	}
	if p.Title != nil && (len(strings.TrimSpace(*p.Title)) == 0 || len([]rune(*p.Title)) > 200) {
		return p, sessiontools.Fail("INVALID_ARGUMENT", "title must contain 1-200 characters")
	}
	return p, nil
}

func collaborationError(id string, err error) sessiontools.Reply {
	var e *sessiontools.Error
	if !errors.As(err, &e) {
		switch {
		case errors.Is(err, ErrThreadActive):
			// ErrThreadActive's text predates non-Codex sessions.
			e = &sessiontools.Error{Code: "SESSION_BUSY", Message: "session is running, archived or cannot accept this action now"}
		case errors.Is(err, ErrWorkspaceWriteActive), errors.Is(err, ErrClaudeCredentialsBusy), errors.Is(err, ErrGeminiCredentialsBusy):
			e = &sessiontools.Error{Code: "SESSION_BUSY", Message: err.Error()}
		case errors.Is(err, sql.ErrNoRows):
			e = &sessiontools.Error{Code: "NOT_FOUND", Message: "session or resource no longer exists"}
		default:
			e = &sessiontools.Error{Code: "INTERNAL_ERROR", Message: "Wio could not complete the request"}
		}
	}
	return sessiontools.Reply{RequestID: id, Status: "failed", Error: e}
}

func (s *Store) authorizeCollaboration(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, grant sessiontools.Grant, target sessiontools.Ref) (CollaborationSession, error) {
	t, err := s.collaborationSession(ctx, tx, target)
	if errors.Is(err, sql.ErrNoRows) {
		var pending CollaborationRequest
		if e := tx.GetContext(ctx, &pending, s.Q("SELECT * FROM session_tool_requests WHERE source_key=? AND target_engine=? AND target_id=? AND phase<>'' AND status IN ('accepted','preparing') LIMIT 1"), source.Ref().Key(), target.Engine, target.ID); e == nil {
			t = CollaborationSession{Engine: target.Engine, ID: target.ID, Title: "Preparing session", Status: "preparing", WorkspaceID: pending.WorkspaceID, ProjectID: source.ProjectID, ServerID: source.ServerID, PreparationPhase: pending.Phase, URL: "/?view=" + target.Engine + "&thread=" + target.ID}
			err = nil
		}
	}
	if err != nil {
		return t, err
	}
	if t.ProjectID != source.ProjectID {
		return t, sessiontools.Fail("OUT_OF_SCOPE", "target belongs to another project")
	}
	if target == source.Ref() {
		return t, nil
	}
	if grant.AllProjectSessions {
		return t, nil
	}
	for _, r := range grant.SessionRefs {
		if r == target {
			return t, nil
		}
	}
	var count int
	err = tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM session_links WHERE source_key=? AND target_key=?"), source.Ref().Key(), target.Key())
	if err == nil && count != 1 {
		err = sessiontools.Fail("OUT_OF_SCOPE", "only your child sessions or explicitly granted sessions may be managed")
	}
	if err == nil && !collaborationWorkspaceAllowed(source, grant, t.WorkspaceID) {
		var arguments string
		if e := tx.GetContext(ctx, &arguments, s.Q("SELECT arguments FROM session_tool_requests WHERE source_key=? AND target_engine=? AND target_id=? AND tool IN ('wio_create_session','wio_fork_session') LIMIT 1"), source.Ref().Key(), target.Engine, target.ID); e != nil {
			return t, e
		}
		var p sessiontools.Params
		_ = json.Unmarshal([]byte(arguments), &p)
		base := p.WorkspaceID
		if base == "" {
			base = source.WorkspaceID
		}
		if !grant.Worktree || p.DirectoryMode != "worktree" || !collaborationWorkspaceAllowed(source, grant, base) {
			err = sessiontools.Fail("OUT_OF_SCOPE", "target workspace is no longer granted")
		}
	}
	return t, err
}

func (s *Store) saveCollaborationRequest(ctx context.Context, tx *sqlx.Tx, r *CollaborationRequest, reply sessiontools.Reply) error {
	reply.RequestID = r.RequestKey
	if data, ok := reply.Data.(map[string]any); ok && r.WorkspaceID != "" {
		if _, reported := data["workspace_id"]; reported {
			data["workspace_id"] = r.WorkspaceID
		}
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	r.Result = string(raw)
	r.Status = reply.Status
	if r.Execution == "" {
		r.Execution = "{}"
	}
	_, err = tx.ExecContext(ctx, s.Q("UPDATE session_tool_requests SET status=?,phase=?,target_engine=?,target_id=?,workspace_id=?,operation_id=?,result=?,execution_json=?,updated_at=? WHERE id=?"), r.Status, r.Phase, r.TargetEngine, r.TargetID, r.WorkspaceID, r.OperationID, r.Result, r.Execution, time.Now().UTC(), r.ID)
	return err
}

func (s *Store) addCollaborationEvent(ctx context.Context, tx *sqlx.Tx, stream, kind string, value any) error {
	if s.isPostgres() {
		if err := lockEventStream(ctx, tx, stream); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) SELECT ?,?,COALESCE(MAX(sequence),0)+1,?,?,? FROM events WHERE stream_id=?"), NewID(), stream, kind, time.Now().UTC(), string(raw), stream)
	return err
}

func refFromKey(key string) sessiontools.Ref {
	engine, id, _ := strings.Cut(key, ":")
	return sessiontools.Ref{Engine: engine, ID: id}
}

// ExecuteCollaboration atomically records requests together with their side
// effects. A lost response can be reconciled without re-running a task.
func (s *Store) ExecuteCollaboration(ctx context.Context, serverID string, call sessiontools.Call) sessiontools.Reply {
	p, err := decodeCollaborationParams(call)
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	defer tx.Rollback()
	source, err := s.collaborationSession(ctx, tx, call.Source)
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	if source.ServerID != serverID || source.ArchivedAt != nil || !source.Busy() || source.OperationID == "" || source.OperationID != call.OperationID {
		return collaborationError(p.RequestID, sessiontools.Fail("OUT_OF_SCOPE", "source is not the active bound session turn"))
	}
	// Lock the root grant before any per-session mutation, serializing limits
	// and revocation across sibling tool calls (also on PostgreSQL).
	root := call.Source.Key()
	var parentRoot string
	err = tx.GetContext(ctx, &parentRoot, s.Q("SELECT root_key FROM session_links WHERE target_key=?"), root)
	if err == nil {
		root = parentRoot
	} else if !errors.Is(err, sql.ErrNoRows) {
		return collaborationError(p.RequestID, err)
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE session_tool_grants SET version=version WHERE source_key=?"), root); err != nil {
		return collaborationError(p.RequestID, err)
	}
	if root != call.Source.Key() {
		if _, err = tx.ExecContext(ctx, s.Q("UPDATE session_tool_grants SET version=version WHERE source_key=?"), call.Source.Key()); err != nil {
			return collaborationError(p.RequestID, err)
		}
	}
	grant, version, err := s.collaborationGrant(ctx, tx, call.Source.Key())
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	if call.Name == "wio_get_context" {
		return sessiontools.Reply{Status: "succeeded", Data: map[string]any{"session": source, "grant": grant, "engines": []string{"codex", "claude", "gemini"}, "limits": map[string]int{"active_children": 2, "total_sessions": 10, "delegation_depth": 2, "wait_ms": 30000}, "shared_directory": true}}
	}
	if !grant.Enabled {
		return collaborationError(p.RequestID, sessiontools.Fail("OUT_OF_SCOPE", "enable model collaboration in this session's settings first"))
	}
	if !sessiontools.Mutating(call.Name) {
		reply, e := s.readCollaboration(ctx, tx, source, grant, call.Name, p)
		if e != nil {
			return collaborationError(p.RequestID, e)
		}
		return reply
	}
	canonical, _ := json.Marshal(p)
	var request CollaborationRequest
	err = tx.GetContext(ctx, &request, s.Q("SELECT * FROM session_tool_requests WHERE source_key=? AND request_key=?"), call.Source.Key(), p.RequestID)
	if err == nil {
		if request.Tool != call.Name || request.Arguments != string(canonical) {
			return collaborationError(p.RequestID, sessiontools.Fail("IDEMPOTENCY_CONFLICT", "request_id was already used with different arguments"))
		}
		var result sessiontools.Reply
		_ = json.Unmarshal([]byte(request.Result), &result)
		return result
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return collaborationError(p.RequestID, err)
	}
	request = CollaborationRequest{ID: NewID(), SourceKey: call.Source.Key(), RequestKey: p.RequestID, Tool: call.Name, Arguments: string(canonical), GrantVersion: version, Status: "accepted"}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO session_tool_requests(id,source_key,request_key,tool,arguments,grant_version,status) VALUES(?,?,?,?,?,?,'accepted')"), request.ID, request.SourceKey, p.RequestID, call.Name, request.Arguments, version)
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	// Failed business actions roll back their partial writes but retain an
	// immutable failed request for replay and audit.
	if _, err = tx.ExecContext(ctx, "SAVEPOINT collaboration_action"); err != nil {
		return collaborationError(p.RequestID, err)
	}
	reply, err := s.mutateCollaboration(ctx, tx, source, grant, root, &request, p)
	if err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT collaboration_action"); rollbackErr != nil {
			return collaborationError(p.RequestID, rollbackErr)
		}
		reply = collaborationError(p.RequestID, err)
		request.Phase = ""
		request.OperationID = ""
	}
	if err = s.saveCollaborationRequest(ctx, tx, &request, reply); err != nil {
		return collaborationError(p.RequestID, err)
	}
	if err = s.addCollaborationEvent(ctx, tx, source.ID, "session.collaboration.request", map[string]any{"request_id": p.RequestID, "tool": call.Name, "status": reply.Status, "target_engine": request.TargetEngine, "target_id": request.TargetID}); err != nil {
		return collaborationError(p.RequestID, err)
	}
	if err = tx.Commit(); err != nil {
		return collaborationError(p.RequestID, err)
	}
	reply.RequestID = p.RequestID
	return reply
}

// ReadCollaborationState is for the browser. It does not require a running CLI.
func (s *Store) ReadCollaborationState(ctx context.Context, ref sessiontools.Ref) (map[string]any, error) {
	source, err := s.CollaborationSession(ctx, ref)
	if err != nil {
		return nil, err
	}
	grant, err := s.CollaborationGrant(ctx, ref)
	if err != nil {
		return nil, err
	}
	var requests []CollaborationRequest
	err = s.DB.SelectContext(ctx, &requests, s.Q("SELECT * FROM session_tool_requests WHERE source_key=? ORDER BY created_at DESC LIMIT 100"), ref.Key())
	if err != nil {
		return nil, err
	}
	for i := range requests {
		var reply sessiontools.Reply
		if json.Unmarshal([]byte(requests[i].Result), &reply) == nil {
			requests[i].Error = reply.Error
		}
	}
	var links []struct {
		Source   string `db:"source_key" json:"source_key"`
		Target   string `db:"target_key" json:"target_key"`
		Relation string `db:"relation" json:"relation"`
	}
	if err = s.DB.SelectContext(ctx, &links, s.Q("SELECT source_key,target_key,relation FROM session_links WHERE source_key=? OR target_key=? ORDER BY created_at"), ref.Key(), ref.Key()); err != nil {
		return nil, err
	}
	items := []any{}
	for _, link := range links {
		other := link.Target
		if other == ref.Key() {
			other = link.Source
		}
		t, e := s.CollaborationSession(ctx, refFromKey(other))
		items = append(items, map[string]any{"source_key": link.Source, "target_key": link.Target, "relation": link.Relation, "session": t, "missing": e != nil})
	}
	var notifications []struct {
		Target string `db:"target_key" json:"target_key"`
		Event  string `db:"unread_event_id" json:"event_id"`
	}
	if err = s.DB.SelectContext(ctx, &notifications, s.Q("SELECT target_key,unread_event_id FROM session_subscriptions WHERE source_key=? AND unread_event_id<>''"), ref.Key()); err != nil {
		return nil, err
	}
	return map[string]any{"session": source, "grant": grant, "requests": requests, "links": items, "notifications": notifications, "supported": buildinfo.SupportsSessionCollaboration(source.AgentVersion)}, nil
}

// Acknowledge only the displayed event, preserving notifications arriving while
// the browser was reading the previous result.
func (s *Store) MarkCollaborationRead(ctx context.Context, ref sessiontools.Ref, target, event string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE session_subscriptions SET unread_event_id='' WHERE source_key=? AND target_key=? AND unread_event_id=?"), ref.Key(), target, event)
	return err
}

func (s *Store) readCollaboration(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, grant sessiontools.Grant, name string, p sessiontools.Params) (sessiontools.Reply, error) {
	reply := sessiontools.Reply{RequestID: p.RequestID, Status: "succeeded"}
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	switch name {
	case "wio_get_request":
		var r CollaborationRequest
		if err := tx.GetContext(ctx, &r, s.Q("SELECT * FROM session_tool_requests WHERE source_key=? AND request_key=?"), source.Ref().Key(), p.RequestID); err != nil {
			return reply, err
		}
		err := json.Unmarshal([]byte(r.Result), &reply)
		return reply, err
	case "wio_list_workspaces":
		var workspaces []Workspace
		if err := tx.SelectContext(ctx, &workspaces, s.Q("SELECT * FROM workspaces WHERE project_id=? AND status='ready' ORDER BY id"), source.ProjectID); err != nil {
			return reply, err
		}
		items := []Workspace{}
		for _, w := range workspaces {
			if collaborationWorkspaceAllowed(source, grant, w.ID) {
				items = append(items, w)
			}
		}
		reply.Data = items
		return reply, nil
	case "wio_list_sessions":
		// Non-nil so an empty page encodes as [] rather than null.
		sessions := []CollaborationSession{}
		query := "SELECT * FROM (" + collaborationSessions + ") sessions WHERE project_id=? AND "
		if p.Archived {
			query += "archived_at IS NOT NULL"
		} else {
			query += "archived_at IS NULL"
		}
		args := []any{source.ProjectID}
		query += " AND (workspace_id=?"
		args = append(args, source.WorkspaceID)
		for _, workspace := range grant.WorkspaceIDs {
			query += " OR workspace_id=?"
			args = append(args, workspace)
		}
		query += " OR EXISTS (SELECT 1 FROM session_links l WHERE l.source_key=? AND l.target_key=sessions.engine||':'||sessions.id)"
		args = append(args, source.Ref().Key())
		for _, ref := range grant.SessionRefs {
			query += " OR (engine=? AND id=?)"
			args = append(args, ref.Engine, ref.ID)
		}
		if grant.AllProjectSessions {
			query += " OR 1=1"
		}
		query += ")"
		if p.Engine != "" {
			query += " AND engine=?"
			args = append(args, p.Engine)
		}
		if p.Status != "" {
			query += " AND status=?"
			args = append(args, p.Status)
		}
		if p.ChildrenOnly {
			query += " AND EXISTS (SELECT 1 FROM session_links l WHERE l.source_key=? AND l.target_key=sessions.engine||':'||sessions.id)"
			args = append(args, source.Ref().Key())
		}
		query += " ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?"
		args = append(args, limit+1, p.Offset)
		if err := tx.SelectContext(ctx, &sessions, s.Q(query), args...); err != nil {
			return reply, err
		}
		more := len(sessions) > limit
		if more {
			sessions = sessions[:limit]
		}
		for i := range sessions {
			sessions[i].URL = "/?view=" + sessions[i].Engine + "&thread=" + sessions[i].ID
			sessions[i].NativeID = ""
			sessions[i].Mode = ""
			sessions[i].Path = ""
		}
		reply.Data = map[string]any{"sessions": sessions, "has_more": more, "next_offset": p.Offset + len(sessions)}
		return reply, nil
	case "wio_wait_sessions":
		if len(p.Sessions) == 0 {
			return reply, sessiontools.Fail("INVALID_ARGUMENT", "sessions must not be empty")
		}
		items := []CollaborationSession{}
		for _, ref := range p.Sessions {
			if ref == source.Ref() {
				return reply, sessiontools.Fail("WAIT_DEPENDENCY", "cannot wait on the calling session")
			}
			t, err := s.authorizeCollaboration(ctx, tx, source, grant, ref)
			if err != nil {
				return reply, err
			}
			if err = s.decorateCollaborationSession(ctx, tx, &t); err != nil {
				return reply, err
			}
			if t.PreparationPhase == "await_source" {
				var args string
				if err = tx.GetContext(ctx, &args, s.Q("SELECT arguments FROM session_tool_requests WHERE source_key=? AND target_engine=? AND target_id=? AND phase='await_source' LIMIT 1"), source.Ref().Key(), ref.Engine, ref.ID); err == nil {
					var deferred sessiontools.Params
					_ = json.Unmarshal([]byte(args), &deferred)
					if deferred.Session == source.Ref() {
						return reply, sessiontools.Fail("WAIT_DEPENDENCY", "this fork requires the calling turn to end first")
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return reply, err
				}
			}
			if t.WorkspaceID == source.WorkspaceID && t.PreparationPhase != "" {
				var locks int
				if err = tx.GetContext(ctx, &locks, s.Q("SELECT COUNT(*) FROM agent_operations WHERE id=? AND workspace_write=1 AND status IN ('queued','delivered','running')"), source.OperationID); err != nil {
					return reply, err
				}
				if locks > 0 {
					return reply, sessiontools.Fail("WAIT_DEPENDENCY", "the calling turn holds the shared workspace; end this turn before waiting, or use directory_mode=worktree to run the child in parallel")
				}
			}
			items = append(items, t)
		}
		reply.Data = items
		reply.Status = "running"
		for _, t := range items {
			if !t.Busy() {
				reply.Status = "succeeded"
				break
			}
			waiting, err := s.collaborationWaitingUser(ctx, tx, t)
			if err != nil {
				return reply, err
			}
			if waiting {
				reply.Status = "waiting_user"
				break
			}
		}
		return reply, nil
	case "wio_get_session", "wio_read_messages", "wio_get_result":
		t, err := s.authorizeCollaboration(ctx, tx, source, grant, p.Session)
		if err != nil {
			return reply, err
		}
		if err = s.decorateCollaborationSession(ctx, tx, &t); err != nil {
			return reply, err
		}
		reply.Session = &p.Session
		if name == "wio_get_session" {
			reply.Data = t
			return reply, nil
		}
		rows, err := s.collaborationMessages(ctx, tx, t.ID, p.After, limit)
		if err != nil {
			return reply, err
		}
		data := map[string]any{"session": t, "messages": rows, "source_material": true, "has_more": len(rows) == limit, "next_after": p.After}
		if len(rows) > 0 {
			data["next_after"] = rows[len(rows)-1].Sequence
		}
		if name == "wio_get_result" {
			opID := p.OperationID
			if opID == "" {
				opID = t.OperationID
			}
			var op Operation
			if opID != "" {
				if err = tx.GetContext(ctx, &op, s.Q(operationSelect+" WHERE id=? AND server_id=?"), opID, t.ServerID); err != nil {
					return reply, err
				}
				if op.Kind != t.Engine+".turn.start" && op.Kind != t.Engine+".turn.rewrite" {
					return reply, sessiontools.Fail("INVALID_ARGUMENT", "operation must identify a model turn")
				}
				var command struct {
					ThreadID string `json:"thread_id"`
					Start    struct {
						ThreadID string `json:"thread_id"`
					} `json:"start"`
				}
				_ = json.Unmarshal([]byte(op.Payload), &command)
				if command.ThreadID != t.ID && command.Start.ThreadID != t.ID {
					return reply, sessiontools.Fail("OUT_OF_SCOPE", "operation does not belong to this session")
				}
				data["operation"] = map[string]any{"id": op.ID, "status": op.Status, "message": op.Result, "result": json.RawMessage(op.ResultData), "completed_at": op.CompletedAt}
				turnStatus, ended, stateErr := s.collaborationTurnState(ctx, tx, t, op)
				if stateErr != nil {
					return reply, stateErr
				}
				data["turn_status"], data["turn_ended"] = turnStatus, ended
				rows, err = s.collaborationTurnMessages(ctx, tx, t.ID, op.ID, p.After, limit)
				if err != nil {
					return reply, err
				}
				data["messages"] = rows
				data["has_more"] = len(rows) == limit
				data["next_after"] = p.After
				if len(rows) > 0 {
					data["next_after"] = rows[len(rows)-1].Sequence
				}
			}
			if opID == "" {
				data["turn_ended"] = false
				data["turn_status"] = "not_started"
			}
			data["model_claim_is_verification"] = false
		}
		reply.Data = data
		return reply, nil
	}
	return reply, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "unsupported tool")
}

func collaborationWorkspaceAllowed(source CollaborationSession, g sessiontools.Grant, id string) bool {
	if id == source.WorkspaceID {
		return true
	}
	for _, allowed := range g.WorkspaceIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (s *Store) collaborationMessages(ctx context.Context, tx *sqlx.Tx, id string, after int64, limit int) ([]protocol.StreamEvent, error) {
	return s.collaborationMessageRange(ctx, tx, id, after, 0, limit)
}

func (s *Store) collaborationTurnMessages(ctx context.Context, tx *sqlx.Tx, id, operation string, after int64, limit int) ([]protocol.StreamEvent, error) {
	start, end, err := s.collaborationTurnBounds(ctx, tx, id, operation)
	if err != nil {
		return nil, err
	}
	if start == 0 {
		return []protocol.StreamEvent{}, nil
	}
	if after < start-1 {
		after = start - 1
	}
	return s.collaborationMessageRange(ctx, tx, id, after, end, limit)
}

func (s *Store) collaborationTurnBounds(ctx context.Context, tx *sqlx.Tx, id, operation string) (int64, int64, error) {
	var starts []struct {
		Sequence int64  `db:"sequence"`
		Payload  string `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &starts, s.Q("SELECT sequence,payload FROM events WHERE stream_id=? AND kind IN ('user.message','claude.user.message','gemini.user.message') ORDER BY sequence"), id); err != nil {
		return 0, 0, err
	}
	var start, end int64
	for _, row := range starts {
		var payload struct {
			Operation string `json:"operation_id"`
		}
		_ = json.Unmarshal([]byte(row.Payload), &payload)
		if start != 0 {
			end = row.Sequence
			break
		}
		if payload.Operation == operation {
			start = row.Sequence
		}
	}
	return start, end, nil
}

func (s *Store) collaborationMessageRange(ctx context.Context, tx *sqlx.Tx, id string, after, before int64, limit int) ([]protocol.StreamEvent, error) {
	var rows []struct {
		ID       string    `db:"event_id"`
		Sequence int64     `db:"sequence"`
		Kind     string    `db:"kind"`
		Payload  string    `db:"payload"`
		At       time.Time `db:"occurred_at"`
	}
	err := tx.SelectContext(ctx, &rows, s.Q("SELECT event_id,sequence,kind,payload,occurred_at FROM events WHERE stream_id=? AND sequence>? AND (?=0 OR sequence<?) ORDER BY sequence LIMIT ?"), id, after, before, before, limit)
	result := []protocol.StreamEvent{}
	budget := 512 << 10
	for _, row := range rows {
		if len(row.Payload) > budget {
			if len(result) > 0 {
				break
			}
			row.Payload = `{"truncated":true,"message":"Event exceeds tool output limit; open the Wio session to inspect it"}`
		}
		budget -= len(row.Payload)
		result = append(result, protocol.StreamEvent{EventID: row.ID, StreamID: id, Sequence: row.Sequence, Kind: row.Kind, Payload: json.RawMessage(row.Payload), OccurredAt: row.At})
	}
	return result, err
}

func (s *Store) collaborationWaitingUser(ctx context.Context, tx sqlx.QueryerContext, t CollaborationSession) (bool, error) {
	var count int
	if t.Engine == "codex" {
		err := sqlx.GetContext(ctx, tx, &count, s.Q("SELECT COUNT(*) FROM approvals WHERE thread_id=? AND status='pending'"), t.ID)
		return count > 0, err
	}
	var events []struct {
		ID      string `db:"event_id"`
		Kind    string `db:"kind"`
		Payload string `db:"payload"`
	}
	err := sqlx.SelectContext(ctx, tx, &events, s.Q("SELECT event_id,kind,payload FROM events WHERE stream_id=? AND kind IN (?,?,?) ORDER BY sequence"), t.ID, t.Engine+".question", t.Engine+".question.answered", t.Engine+".question.cancelled")
	if err != nil {
		return false, err
	}
	pending := map[string]bool{}
	for _, event := range events {
		var p struct {
			RequestID   string `json:"request_id"`
			OperationID string `json:"operation_id"`
		}
		_ = json.Unmarshal([]byte(event.Payload), &p)
		id := p.RequestID
		if id == "" {
			id = event.ID
		}
		if event.Kind == t.Engine+".question" {
			if p.OperationID == t.OperationID {
				pending[id] = true
			}
		} else {
			delete(pending, id)
		}
	}
	return t.Status == "running" && len(pending) > 0, nil
}
