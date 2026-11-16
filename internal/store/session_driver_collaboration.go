package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/geminiapi"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessiontools"
)

type collaborationEngineDriver interface {
	SetExecution(context.Context, *sqlx.Tx, sessiontools.Ref, string, string, string) error
	DeferredExecution() bool
	Create(context.Context, *sqlx.Tx, sessiontools.Ref, string, string, string, string, string) error
	Send(context.Context, *sqlx.Tx, CollaborationSession, string) (string, error)
	Steer(context.Context, *sqlx.Tx, CollaborationSession, string, string) (string, error)
	Interrupt(context.Context, *sqlx.Tx, CollaborationSession) (string, error)
	Fork(context.Context, *sqlx.Tx, CollaborationSession, string, *protocol.GitWorktreeCreateCommand, string) (string, error)
	Pin(context.Context, *sqlx.Tx, CollaborationSession, *time.Time) (bool, error)
	TurnWritesWorkspace() bool
	TracksNativeTurn(string) bool
}

func (s *Store) collaborationDriver(engine string) collaborationEngineDriver {
	return map[string]collaborationEngineDriver{
		session.Codex: codexCollaborationDriver{s}, session.Claude: claudeCollaborationDriver{s}, session.Gemini: geminiCollaborationDriver{s},
	}[engine]
}

func (s *Store) createCollaborationSessionTx(ctx context.Context, tx *sqlx.Tx, ref sessiontools.Ref, workspaceID, title, model, mode, effort string) error {
	d := s.collaborationDriver(ref.Engine)
	if d == nil {
		return sessiontools.Fail("INVALID_ARGUMENT", "invalid engine")
	}
	return d.Create(ctx, tx, ref, workspaceID, title, model, mode, effort)
}

func (s *Store) collaborationSend(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", sessiontools.Fail("INVALID_ARGUMENT", "prompt must not be empty")
	}
	if t.ArchivedAt != nil || t.Busy() {
		return "", ErrThreadActive
	}
	return s.collaborationDriver(t.Engine).Send(ctx, tx, t, prompt)
}

func (s *Store) collaborationSteer(ctx context.Context, tx *sqlx.Tx, t CollaborationSession, prompt, key string) (string, error) {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 64<<10 {
		return "", sessiontools.Fail("INVALID_ARGUMENT", "steering requires a prompt of at most 64 KiB")
	}
	d := s.collaborationDriver(t.Engine)
	if d == nil {
		return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "steering is unavailable")
	}
	return d.Steer(ctx, tx, t, prompt, key)
}

func (s *Store) collaborationFork(ctx context.Context, tx *sqlx.Tx, source CollaborationSession, target string, worktree *protocol.GitWorktreeCreateCommand, eventID string) (string, error) {
	d := s.collaborationDriver(source.Engine)
	if d == nil {
		return "", sessiontools.Fail("CAPABILITY_UNSUPPORTED", "native fork is unavailable")
	}
	if d.TurnWritesWorkspace() || worktree != nil {
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
	return d.Fork(ctx, tx, source, target, worktree, eventID)
}

func (s *Store) collaborationClaude(ctx context.Context, tx *sqlx.Tx, id string) (ClaudeThread, error) {
	var t ClaudeThread
	err := tx.GetContext(ctx, &t, s.Q(claudeThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	t.setSteerCapability()
	t.QuestionsSupported = t.Capabilities.Questions
	return t, err
}

func (s *Store) collaborationGemini(ctx context.Context, tx *sqlx.Tx, id string) (GeminiThread, error) {
	var t GeminiThread
	err := tx.GetContext(ctx, &t, s.Q(geminiThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	t.setCapabilities()
	t.SteerSupported = t.Capabilities.Steer
	return t, err
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
	op, err := s.queueCollaborationOperation(ctx, tx, CollaborationSession{ID: t.ID, ServerID: t.ServerID, ProjectID: t.ProjectID, WorkspaceID: t.WorkspaceID}, protocol.OpCodexTurnStart, command, false)
	if err != nil {
		return "", protocol.StreamEvent{}, err
	}
	raw, _ := json.Marshal(map[string]any{"text": command.Prompt, "image_count": len(command.Images), "operation_id": op})
	event := protocol.StreamEvent{EventID: NewID(), StreamID: t.ID, Kind: protocol.EventUserMessage, Payload: raw, OccurredAt: time.Now().UTC()}
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
		err := tx.GetContext(ctx, &configured, s.Q("SELECT COALESCE(NULLIF(b.model,''),p.model,'') FROM workspaces w LEFT JOIN "+claudeEngineBinding+" b ON b.server_id=w.server_id LEFT JOIN credential_profiles p ON p.id=b.profile_id WHERE w.id=?"), workspace)
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
			if _, err = tx.ExecContext(ctx, s.Q("UPDATE scheduled_tasks SET last_run_status='cancelled',last_run_message='cancelled before Codex turn started',updated_at=? WHERE engine='codex' AND last_operation_id=?"), time.Now().UTC(), t.OperationID); err != nil {
				return "", err
			}
			return t.OperationID, s.addCollaborationEvent(ctx, tx, t.ID, protocol.EventCodexTurnCancelled, map[string]string{"operation_id": t.OperationID, "source": "control"})
		}
		// Delivered turns can be stopped before the native accepted event arrives.
		if t.Status == "queued" {
			return s.queueCollaborationOperation(ctx, tx, t, protocol.OpCodexTurnInterrupt, protocol.InterruptTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID}, false)
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
		return s.queueCollaborationOperation(ctx, tx, t, protocol.OpCodexTurnInterrupt, protocol.InterruptTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID, TurnID: turn}, false)
	}
	return s.queueCollaborationOperation(ctx, tx, t, protocol.OpCodexTurnSteer, protocol.SteerTurnCommand{ThreadID: t.ID, CodexThread: t.NativeID, ExpectedTurnID: turn, Prompt: prompt}, false)
}

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
	err := sqlx.SelectContext(ctx, tx, &events, s.Q("SELECT event_id,kind,payload FROM events WHERE stream_id=? AND kind IN (?,?,?) ORDER BY sequence"), t.ID, t.Engine+protocol.EventQuestionSuffix, t.Engine+protocol.EventQuestionAnsweredSuffix, t.Engine+protocol.EventQuestionCancelledSuffix)
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
		if event.Kind == t.Engine+protocol.EventQuestionSuffix {
			if p.OperationID == t.OperationID {
				pending[id] = true
			}
		} else {
			delete(pending, id)
		}
	}
	return t.Status == "running" && len(pending) > 0, nil
}
