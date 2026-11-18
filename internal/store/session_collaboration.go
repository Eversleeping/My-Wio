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

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessionservice/core"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type CollaborationSession = core.CollaborationSession

var collaborationSessions = "SELECT engine,id,title,status,workspace_id,project_id,server_id,path,model,mode,effort,native_id,operation_id,agent_version,declared_capabilities,archived_at,updated_at FROM (" + aiSessionRows + ") session_rows WHERE server_revoked_at IS NULL"

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
	global, version, err := s.globalCollaboration(ctx, db)
	if err != nil {
		return sessiontools.Grant{}, 0, err
	}
	if version != 0 {
		var depth int
		err = sqlx.GetContext(ctx, db, &depth, s.Q("SELECT depth FROM session_links WHERE target_key=?"), key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return global, -version, err
		}
		if depth > 0 && !global.Delegate {
			global.Enabled = false
		}
		return global, -version, nil
	}
	var row struct {
		JSON          string `db:"grant_json"`
		Version       int    `db:"version"`
		Parent        string `db:"parent_key"`
		ParentVersion int    `db:"parent_version"`
	}
	err = sqlx.GetContext(ctx, db, &row, s.Q("SELECT grant_json,version,parent_key,parent_version FROM session_tool_grants WHERE source_key=?"), key)
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
	// Display preferences are not authorization. Saving only them keeps the
	// version and parent link, so pending requests, auto-continuation and
	// delegated children stay valid.
	var current struct {
		JSON    string `db:"grant_json"`
		Version int    `db:"version"`
	}
	err = sqlx.GetContext(ctx, s.DB, &current, s.Q("SELECT grant_json,version FROM session_tool_grants WHERE source_key=?"), ref.Key())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var stored sessiontools.Grant
		if json.Unmarshal([]byte(current.JSON), &stored) == nil && collaborationScope(stored) == collaborationScope(grant) {
			result, e := s.DB.ExecContext(ctx, s.Q("UPDATE session_tool_grants SET grant_json=?,updated_at=? WHERE source_key=? AND version=?"), string(raw), time.Now().UTC(), ref.Key(), current.Version)
			if e != nil {
				return e
			}
			if n, _ := result.RowsAffected(); n == 1 {
				return nil
			}
		}
	}
	_, err = s.DB.ExecContext(ctx, s.Q(`INSERT INTO session_tool_grants(source_key,grant_json,version,updated_at) VALUES(?,?,1,?) ON CONFLICT(source_key) DO UPDATE SET grant_json=excluded.grant_json,parent_key='',parent_version=0,version=session_tool_grants.version+1,updated_at=excluded.updated_at`), ref.Key(), string(raw), time.Now().UTC())
	return err
}

// collaborationScope encodes the authorization part of a grant for comparison.
// Display preferences are left out, and empty lists equal missing ones.
func collaborationScope(g sessiontools.Grant) string {
	g.ShowUnreadResults, g.ShowCreationResults = false, false
	if len(g.WorkspaceIDs) == 0 {
		g.WorkspaceIDs = nil
	}
	if len(g.SessionRefs) == 0 {
		g.SessionRefs = nil
	}
	if len(g.TargetModes) == 0 {
		g.TargetModes = nil
	}
	raw, _ := json.Marshal(g)
	return string(raw)
}

type CollaborationRequest = core.CollaborationRequest

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
	if p.Engine != "" && !session.ValidEngine(p.Engine) {
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
		case errors.Is(err, ErrStateConflict):
			e = &sessiontools.Error{Code: "STATE_CONFLICT", Message: err.Error()}
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
	if err == nil && !collaborationWorkspaceAllowed(source, grant, t.WorkspaceID, t.ProjectID) {
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
		allowed, e := s.collaborationWorkspaceGranted(ctx, tx, source, grant, base)
		if e != nil {
			return t, e
		}
		if !grant.Worktree || p.DirectoryMode != "worktree" || !allowed {
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
	if sessiontools.IsTaskTool(call.Name) {
		return s.ExecuteTaskTool(ctx, serverID, call)
	}
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
	if sessiontools.Mutating(call.Name) {
		if err := s.taskMutationGuard(ctx, tx, source.Engine, source.ID); err != nil {
			return collaborationError(p.RequestID, err)
		}
	}
	// Look up root grant key for per-session limits and delegation
	root := call.Source.Key()
	var parentRoot string
	err = tx.GetContext(ctx, &parentRoot, s.Q("SELECT root_key FROM session_links WHERE target_key=?"), root)
	if err == nil {
		root = parentRoot
	} else if !errors.Is(err, sql.ErrNoRows) {
		return collaborationError(p.RequestID, err)
	}

	grant, version, err := s.collaborationGrant(ctx, tx, call.Source.Key())
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	if call.Name == "wio_get_context" {
		return sessiontools.Reply{Status: "succeeded", Data: map[string]any{"session": source, "grant": grant, "engines": []string{session.Codex, session.Claude, session.Gemini}, "limits": map[string]any{"active_children": 2, "total_sessions": nil, "delegation_depth": 2, "wait_ms": 30000}, "shared_directory": true}}
	}
	if !grant.Enabled {
		return collaborationError(p.RequestID, sessiontools.Fail("OUT_OF_SCOPE", "enable model collaboration on the global Settings page first"))
	}
	if !sessiontools.Mutating(call.Name) {
		reply, observed, e := s.readCollaboration(ctx, tx, source, grant, call.Name, p)
		if e != nil {
			return collaborationError(p.RequestID, e)
		}
		// Reads write nothing, except to record that the caller has now seen a
		// child's turn end; see observeCollaborationResult.
		if observed {
			if e = tx.Commit(); e != nil {
				return collaborationError(p.RequestID, e)
			}
		}
		return reply
	}

	// Lock the root grant before any per-session mutation, serializing limits
	// and revocation across sibling tool calls (also on PostgreSQL).
	if err = s.lockCollaborationGrant(ctx, tx, root); err != nil {
		return collaborationError(p.RequestID, err)
	}
	if root != call.Source.Key() {
		if _, err = tx.ExecContext(ctx, s.Q("UPDATE session_tool_grants SET version=version WHERE source_key=?"), call.Source.Key()); err != nil {
			return collaborationError(p.RequestID, err)
		}
	}
	grant, version, err = s.collaborationGrant(ctx, tx, call.Source.Key())
	if err != nil {
		return collaborationError(p.RequestID, err)
	}
	if !grant.Enabled {
		return collaborationError(p.RequestID, sessiontools.Fail("OUT_OF_SCOPE", "enable model collaboration on the global Settings page first"))
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
	if err = s.addCollaborationEvent(ctx, tx, source.ID, protocol.EventSessionCollaborationRequest, map[string]any{"request_id": p.RequestID, "tool": call.Name, "status": reply.Status, "target_engine": request.TargetEngine, "target_id": request.TargetID}); err != nil {
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
	return map[string]any{"session": source, "grant": grant, "requests": requests, "links": items, "notifications": notifications, "supported": capabilities.Supports(protocol.CapabilitySessionCollaboration, source.AgentVersion, source.DeclaredCapabilities)}, nil
}

// Acknowledge only the displayed event, preserving notifications arriving while
// the browser was reading the previous result.
func (s *Store) MarkCollaborationRead(ctx context.Context, ref sessiontools.Ref, target, event string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE session_subscriptions SET unread_event_id='' WHERE source_key=? AND target_key=? AND unread_event_id=?"), ref.Key(), target, event)
	return err
}

// readCollaboration serves the read-only tools. The boolean result reports
// that the reply told the caller a child's latest turn has ended and that this
// was recorded in the transaction, which the caller must then commit.
func (s *Store) readCollaboration(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, grant sessiontools.Grant, name string, p sessiontools.Params) (sessiontools.Reply, bool, error) {
	reply := sessiontools.Reply{RequestID: p.RequestID, Status: "succeeded"}
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	switch name {
	case "wio_get_request":
		var r CollaborationRequest
		if err := tx.GetContext(ctx, &r, s.Q("SELECT * FROM session_tool_requests WHERE source_key=? AND request_key=?"), source.Ref().Key(), p.RequestID); err != nil {
			return reply, false, err
		}
		err := json.Unmarshal([]byte(r.Result), &reply)
		return reply, false, err
	case "wio_list_workspaces":
		var workspaces []Workspace
		if err := tx.SelectContext(ctx, &workspaces, s.Q("SELECT * FROM workspaces WHERE project_id=? AND status='ready' ORDER BY id"), source.ProjectID); err != nil {
			return reply, false, err
		}
		items := []Workspace{}
		for _, w := range workspaces {
			if collaborationWorkspaceAllowed(source, grant, w.ID, w.ProjectID) {
				items = append(items, w)
			}
		}
		reply.Data = items
		return reply, false, nil
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
			return reply, false, err
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
		return reply, false, nil
	case "wio_wait_sessions":
		if len(p.Sessions) == 0 {
			return reply, false, sessiontools.Fail("INVALID_ARGUMENT", "sessions must not be empty")
		}
		items := []CollaborationSession{}
		for _, ref := range p.Sessions {
			if ref == source.Ref() {
				return reply, false, sessiontools.Fail("WAIT_DEPENDENCY", "cannot wait on the calling session")
			}
			t, err := s.authorizeCollaboration(ctx, tx, source, grant, ref)
			if err != nil {
				return reply, false, err
			}
			if err = s.decorateCollaborationSession(ctx, tx, &t); err != nil {
				return reply, false, err
			}
			if t.PreparationPhase == "await_source" {
				var args string
				if err = tx.GetContext(ctx, &args, s.Q("SELECT arguments FROM session_tool_requests WHERE source_key=? AND target_engine=? AND target_id=? AND phase='await_source' LIMIT 1"), source.Ref().Key(), ref.Engine, ref.ID); err == nil {
					var deferred sessiontools.Params
					_ = json.Unmarshal([]byte(args), &deferred)
					if deferred.Session == source.Ref() {
						return reply, false, sessiontools.Fail("WAIT_DEPENDENCY", "this fork requires the calling turn to end first")
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return reply, false, err
				}
			}
			if t.WorkspaceID == source.WorkspaceID && t.PreparationPhase != "" {
				var locks int
				if err = tx.GetContext(ctx, &locks, s.Q("SELECT COUNT(*) FROM agent_operations WHERE id=? AND workspace_write=1 AND status IN ('queued','delivered','running')"), source.OperationID); err != nil {
					return reply, false, err
				}
				if locks > 0 {
					return reply, false, sessiontools.Fail("WAIT_DEPENDENCY", "the calling turn holds the shared workspace; end this turn before waiting, or use directory_mode=worktree to run the child in parallel")
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
				return reply, false, err
			}
			if waiting {
				reply.Status = "waiting_user"
				break
			}
		}
		observed := false
		for _, t := range items {
			seen, err := s.observeCollaborationResult(ctx, tx, source, t)
			if err != nil {
				return reply, false, err
			}
			observed = observed || seen
		}
		return reply, observed, nil
	case "wio_get_session", "wio_read_messages", "wio_get_result":
		t, err := s.authorizeCollaboration(ctx, tx, source, grant, p.Session)
		if err != nil {
			return reply, false, err
		}
		if err = s.decorateCollaborationSession(ctx, tx, &t); err != nil {
			return reply, false, err
		}
		reply.Session = &p.Session
		// Every reply below reports the session's state. Looking at an older
		// turn says nothing about the latest one, so that does not count.
		observed := false
		if name != "wio_get_result" || p.OperationID == "" || p.OperationID == t.OperationID {
			if observed, err = s.observeCollaborationResult(ctx, tx, source, t); err != nil {
				return reply, false, err
			}
		}
		if name == "wio_get_session" {
			reply.Data = t
			return reply, observed, nil
		}
		rows := []protocol.StreamEvent{}
		hasMore, turnSpecific := false, false
		data := map[string]any{"session": t, "source_material": true, "next_after": p.After}
		if name == "wio_get_result" {
			opID := p.OperationID
			if opID == "" {
				opID = t.OperationID
			}
			var op Operation
			if opID != "" {
				if err = tx.GetContext(ctx, &op, s.Q(operationSelect+" WHERE id=? AND server_id=?"), opID, t.ServerID); err != nil {
					return reply, false, err
				}
				if op.Kind != t.Engine+protocol.OpTurnStartSuffix && op.Kind != t.Engine+protocol.OpTurnRewriteSuffix {
					return reply, false, sessiontools.Fail("INVALID_ARGUMENT", "operation must identify a model turn")
				}
				var command struct {
					ThreadID string `json:"thread_id"`
					Start    struct {
						ThreadID string `json:"thread_id"`
					} `json:"start"`
				}
				_ = json.Unmarshal([]byte(op.Payload), &command)
				if command.ThreadID != t.ID && command.Start.ThreadID != t.ID {
					return reply, false, sessiontools.Fail("OUT_OF_SCOPE", "operation does not belong to this session")
				}
				data["operation"] = map[string]any{"id": op.ID, "status": op.Status, "message": op.Result, "result": json.RawMessage(op.ResultData), "completed_at": op.CompletedAt}
				turnStatus, ended, stateErr := s.collaborationTurnState(ctx, tx, t, op)
				if stateErr != nil {
					return reply, false, stateErr
				}
				data["turn_status"], data["turn_ended"] = turnStatus, ended
				rows, hasMore, err = s.collaborationTurnMessages(ctx, tx, t.ID, op.ID, p.After, limit)
				if err != nil {
					return reply, false, err
				}
				turnSpecific = true
			}
			if opID == "" {
				data["turn_ended"] = false
				data["turn_status"] = "not_started"
			}
			data["model_claim_is_verification"] = false
		}
		if !turnSpecific {
			rows, hasMore, err = s.collaborationMessages(ctx, tx, t.ID, p.After, limit)
			if err != nil {
				return reply, false, err
			}
		}
		data["messages"], data["has_more"] = rows, hasMore
		if len(rows) > 0 {
			data["next_after"] = rows[len(rows)-1].Sequence
		}
		reply.Data = data
		return reply, observed, nil
	}
	return reply, false, sessiontools.Fail("CAPABILITY_UNSUPPORTED", "unsupported tool")
}

func collaborationWorkspaceAllowed(source CollaborationSession, g sessiontools.Grant, id, projectID string) bool {
	return core.WorkspaceAllowed(source, g, id, projectID)
}

// collaborationWorkspaceGranted resolves the workspace's project before
// checking the grant, so callers holding only an ID cannot widen the scope.
func (s *Store) collaborationWorkspaceGranted(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, g sessiontools.Grant, id string) (bool, error) {
	if collaborationWorkspaceAllowed(source, g, id, "") {
		return true, nil
	}
	if !g.AllProjectSessions {
		return false, nil
	}
	var project string
	err := tx.GetContext(ctx, &project, s.Q("SELECT project_id FROM workspaces WHERE id=?"), id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && collaborationWorkspaceAllowed(source, g, id, project), err
}

func (s *Store) collaborationMessages(ctx context.Context, tx *sqlx.Tx, id string, after int64, limit int) ([]protocol.StreamEvent, bool, error) {
	return s.collaborationMessageRange(ctx, tx, id, after, 0, limit)
}

func (s *Store) collaborationTurnMessages(ctx context.Context, tx *sqlx.Tx, id, operation string, after int64, limit int) ([]protocol.StreamEvent, bool, error) {
	start, end, err := s.collaborationTurnBounds(ctx, tx, id, operation)
	if err != nil {
		return nil, false, err
	}
	if start == 0 {
		return []protocol.StreamEvent{}, false, nil
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
			Steer     bool   `json:"steer"`
		}
		_ = json.Unmarshal([]byte(row.Payload), &payload)
		if payload.Steer {
			continue
		}
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

func (s *Store) collaborationMessageRange(ctx context.Context, tx *sqlx.Tx, id string, after, before int64, limit int) ([]protocol.StreamEvent, bool, error) {
	var rows []struct {
		ID       string    `db:"event_id"`
		Sequence int64     `db:"sequence"`
		Kind     string    `db:"kind"`
		Payload  string    `db:"payload"`
		At       time.Time `db:"occurred_at"`
	}
	err := tx.SelectContext(ctx, &rows, s.Q("SELECT event_id,sequence,kind,payload,occurred_at FROM events WHERE stream_id=? AND sequence>? AND (?=0 OR sequence<?) ORDER BY sequence LIMIT ?"), id, after, before, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	result := []protocol.StreamEvent{}
	budget := 512 << 10
	hasMore := len(rows) > limit
	for i, row := range rows {
		if i == limit {
			break
		}
		if len(row.Payload) > budget {
			if len(result) > 0 {
				hasMore = true
				break
			}
			row.Payload = `{"truncated":true,"message":"Event exceeds tool output limit; open the Wio session to inspect it"}`
		}
		budget -= len(row.Payload)
		result = append(result, protocol.StreamEvent{EventID: row.ID, StreamID: id, Sequence: row.Sequence, Kind: row.Kind, Payload: json.RawMessage(row.Payload), OccurredAt: row.At})
	}
	return result, hasMore, nil
}
