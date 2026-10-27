package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

type GeminiThread struct {
	ThinkingLevel     string     `db:"thinking_level" json:"thinking_level"`
	InputSupported    bool       `db:"-" json:"input_supported"`
	ThinkingSupported bool       `db:"-" json:"thinking_supported"`
	AgentVersion      string     `db:"agent_version" json:"-"`
	RewriteSupported  bool       `db:"-" json:"rewrite_supported"`
	ForkSupported     bool       `db:"-" json:"fork_supported"`
	PinnedAt          *time.Time `db:"pinned_at" json:"pinned_at"`
	ProjectPinnedAt   *time.Time `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt   *time.Time `db:"project_hidden_at" json:"project_hidden_at"`
	ID                string     `db:"id" json:"id"`
	WorkspaceID       string     `db:"workspace_id" json:"workspace_id"`
	SessionID         string     `db:"session_id" json:"session_id"`
	Title             string     `db:"title" json:"title"`
	Model             string     `db:"model" json:"model"`
	ApprovalMode      string     `db:"approval_mode" json:"approval_mode"`
	Status            string     `db:"status" json:"status"`
	OperationID       string     `db:"operation_id" json:"operation_id"`
	ArchivedAt        *time.Time `db:"archived_at" json:"archived_at"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at" json:"updated_at"`
	Path              string     `db:"path" json:"path"`
	ProjectID         string     `db:"project_id" json:"project_id"`
	ProjectName       string     `db:"project_name" json:"project_name"`
	ServerID          string     `db:"server_id" json:"server_id"`
	ServerName        string     `db:"server_name" json:"server_name"`
	ServerStatus      string     `db:"server_status" json:"server_status"`
}

const geminiThreadSelect = `SELECT t.*,s.agent_version,w.path,w.project_id,` + projectServerName + ` AS project_name,` + projectServerPin + ` AS project_pinned_at,` + projectServerHidden + ` AS project_hidden_at,w.server_id,s.name AS server_name,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END AS server_status
 FROM gemini_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN projects p ON p.id=w.project_id JOIN servers s ON s.id=w.server_id` + projectServerJoin

func (s *Store) GeminiThreads(ctx context.Context, archived bool) ([]GeminiThread, error) {
	out := []GeminiThread{}
	condition := "t.archived_at IS NULL"
	if archived {
		condition = "t.archived_at IS NOT NULL"
	}
	err := s.DB.SelectContext(ctx, &out, s.Q(geminiThreadSelect+" WHERE s.revoked_at IS NULL AND "+condition+" ORDER BY CASE WHEN ps.pinned_at IS NULL THEN 1 ELSE 0 END,ps.pinned_at DESC,CASE WHEN t.pinned_at IS NULL THEN 1 ELSE 0 END,t.pinned_at DESC,t.updated_at DESC LIMIT 200"), time.Now().UTC().Add(-ServerOnlineGracePeriod))
	for i := range out {
		out[i].InputSupported = buildinfo.SupportsGeminiInput(out[i].AgentVersion)
		out[i].ThinkingSupported = buildinfo.SupportsGeminiThinking(out[i].AgentVersion)
		out[i].ForkSupported = buildinfo.SupportsGeminiFork(out[i].AgentVersion)
		out[i].RewriteSupported = buildinfo.SupportsGeminiRewrite(out[i].AgentVersion)
	}
	return out, err
}

func (s *Store) GeminiThread(ctx context.Context, id string) (GeminiThread, error) {
	var thread GeminiThread
	err := s.DB.GetContext(ctx, &thread, s.Q(geminiThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	thread.InputSupported = buildinfo.SupportsGeminiInput(thread.AgentVersion)
	thread.ThinkingSupported = buildinfo.SupportsGeminiThinking(thread.AgentVersion)
	thread.ForkSupported = buildinfo.SupportsGeminiFork(thread.AgentVersion)
	thread.RewriteSupported = buildinfo.SupportsGeminiRewrite(thread.AgentVersion)
	return thread, err
}

func (s *Store) CreateGeminiThread(ctx context.Context, workspaceID, model, mode string, thinking ...string) (GeminiThread, error) {
	level := ""
	if len(thinking) > 0 {
		level = thinking[0]
	}
	id := NewID()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO gemini_threads(id,workspace_id,model,approval_mode,thinking_level,created_at,updated_at)
 SELECT ?,w.id,?,?,?,?,? FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), id, model, mode, level, time.Now().UTC(), time.Now().UTC(), workspaceID)
	if err != nil {
		return GeminiThread{}, err
	}
	return s.GeminiThread(ctx, id)
}

// QueueGeminiTurn claims the session, reserves the workspace and saves the user
// message in one transaction, so concurrent sends cannot create duplicate turns.
func (s *Store) QueueGeminiTurn(ctx context.Context, thread GeminiThread, prompt, model, mode string, thinking ...string) (protocol.StreamEvent, error) {
	return s.queueGeminiTurn(ctx, thread, prompt, model, mode, "", nil, thinking...)
}

func (s *Store) QueueGeminiRewrite(ctx context.Context, thread GeminiThread, eventID, prompt, model, mode string, thinking ...string) (protocol.StreamEvent, error) {
	return s.queueGeminiTurn(ctx, thread, prompt, model, mode, eventID, nil, thinking...)
}

// QueueGeminiInput shares the transactional lock and native rewrite path.
func (s *Store) QueueGeminiInput(ctx context.Context, thread GeminiThread, eventID, prompt, model, mode, thinking string, images []protocol.TurnImage) (protocol.StreamEvent, error) {
	return s.queueGeminiTurn(ctx, thread, prompt, model, mode, eventID, images, thinking)
}

func (s *Store) queueGeminiTurn(ctx context.Context, thread GeminiThread, prompt, model, mode, editEventID string, images []protocol.TurnImage, thinking ...string) (protocol.StreamEvent, error) {
	level := thread.ThinkingLevel
	if len(thinking) > 0 {
		level = thinking[0]
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	defer tx.Rollback()

	// Serialize configuration changes with newly queued turns on this server.
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), thread.ServerID); err != nil {
		return protocol.StreamEvent{}, err
	}
	var configuring int
	if err := tx.GetContext(ctx, &configuring, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('gemini.credentials.configure','cli.install') AND status IN ('queued','delivered','running')"), thread.ServerID); err != nil {
		return protocol.StreamEvent{}, err
	}
	if configuring != 0 {
		return protocol.StreamEvent{}, ErrGeminiCredentialsBusy
	}
	opID, now := NewID(), time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.Q(`UPDATE gemini_threads SET status='queued',operation_id=?,model=?,approval_mode=?,thinking_level=?,updated_at=?,title=CASE WHEN title='Gemini' THEN ? ELSE title END
 WHERE id=? AND archived_at IS NULL AND status NOT IN ('queued','running','interrupting')`), opID, model, mode, level, now, truncateGeminiTitle(prompt), thread.ID)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	if count != 1 {
		return protocol.StreamEvent{}, ErrThreadActive
	}
	command := protocol.GeminiTurnCommand{ThreadID: thread.ID, Workspace: thread.Path, SessionID: thread.SessionID, Prompt: prompt, Model: model, ApprovalMode: mode, ThinkingLevel: level}
	if err := tx.GetContext(ctx, &command.SessionID, s.Q("SELECT session_id FROM gemini_threads WHERE id=?"), thread.ID); err != nil {
		return protocol.StreamEvent{}, err
	}
	var agentVersion string
	if err := tx.GetContext(ctx, &agentVersion, s.Q("SELECT agent_version FROM servers WHERE id=?"), thread.ServerID); err != nil {
		return protocol.StreamEvent{}, err
	}
	command.Interactive = buildinfo.SupportsGeminiInput(agentVersion)
	if len(images) > 0 && !command.Interactive {
		return protocol.StreamEvent{}, errors.New("update Agent to support Gemini images")
	}
	command.Images = images
	kind := "gemini.turn.start"
	if editEventID != "" {
		target, err := geminiRewriteTarget(ctx, tx, s, thread.ID, editEventID)
		if err != nil {
			return protocol.StreamEvent{}, err
		}
		command.Rewrite = &target
		kind = "gemini.turn.rewrite"
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at)
 VALUES(?,?,?,?,?,?,1,?,?)`), opID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, kind, string(payload), "gemini:"+opID, now)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	if editEventID != "" {
		return protocol.StreamEvent{}, tx.Commit()
	}
	message, _ := json.Marshal(map[string]any{"text": prompt, "operation_id": opID, "model": model, "thinking_level": level, "images": images})
	event := protocol.StreamEvent{EventID: NewID(), StreamID: thread.ID, Kind: "gemini.user.message", OccurredAt: now, Payload: message}
	err = tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), thread.ID)
	if err != nil {
		return event, err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, now, string(message))
	if err != nil {
		return event, err
	}
	return event, tx.Commit()
}

func truncateGeminiTitle(prompt string) string {
	if prompt == "" {
		return "Gemini"
	}
	chars := []rune(prompt)
	if len(chars) > 60 {
		chars = chars[:60]
	}
	return string(chars)
}

func (s *Store) InterruptGeminiTurn(ctx context.Context, thread GeminiThread) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Cancel an undispatched task locally. An in-flight task must acknowledge its
	// cancellation before releasing the workspace or accepting another prompt.
	result, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='cancelled',completed_at=? WHERE id=? AND status='queued'"), time.Now().UTC(), thread.OperationID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	status := "interrupting"
	if count == 1 {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE gemini_scheduled_tasks SET last_run_status='cancelled',updated_at=? WHERE last_operation_id=? AND last_run_status IN ('queued','running')"), time.Now().UTC(), thread.OperationID); err != nil {
			return err
		}
		status = "interrupted"
	}
	result, err = tx.ExecContext(ctx, s.Q("UPDATE gemini_threads SET status=?,updated_at=? WHERE id=? AND operation_id=? AND status IN ('queued','running')"), status, time.Now().UTC(), thread.ID, thread.OperationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrThreadActive
	}
	if count == 0 {
		payload, _ := json.Marshal(protocol.GeminiInterruptCommand{ThreadID: thread.ID, OperationID: thread.OperationID})
		id := NewID()
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at)
 VALUES(?,?,?,?,'gemini.turn.interrupt',?,?,?)`), id, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(payload), "gemini-interrupt:"+id, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteGeminiThread(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("DELETE FROM gemini_threads WHERE id=? AND status NOT IN ('queued','running','interrupting')"), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrThreadActive
	}
	if _, err = tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=?"), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateGeminiThread(ctx context.Context, id, title string, archived bool) error {
	var at *time.Time
	if archived {
		now := time.Now().UTC()
		at = &now
	}
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE gemini_threads SET title=?,archived_at=?,updated_at=? WHERE id=? AND status NOT IN ('queued','running','interrupting')"), title, at, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrThreadActive
	}
	return nil
}

// CompleteGeminiTurn only accepts the currently claimed operation. Late results
// cannot finish a later turn or overwrite its model/session state.
func (s *Store) CompleteGeminiTurn(ctx context.Context, id, opID, status string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE gemini_threads SET status=CASE WHEN status='interrupting' THEN 'interrupted' ELSE ? END,updated_at=? WHERE id=? AND operation_id=?`), status, time.Now().UTC(), id, opID)
	return err
}

// A native timestamp is saved verbatim in gemini.init. Select a turn using the
// Agent's clock; control-plane timestamps are deliberately not compared to it.
func geminiRewriteTarget(ctx context.Context, tx *sqlx.Tx, s *Store, threadID, eventID string) (protocol.GeminiRewriteTarget, error) {
	var events []struct {
		ID      string `db:"event_id"`
		Kind    string `db:"kind"`
		Payload string `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &events, s.Q("SELECT event_id,kind,payload FROM events WHERE stream_id=? AND kind IN ('gemini.user.message','gemini.init') ORDER BY sequence"), threadID); err != nil {
		return protocol.GeminiRewriteTarget{}, err
	}
	target := protocol.GeminiRewriteTarget{EventID: eventID}
	operationID := ""
	found := false
	for _, event := range events {
		var p struct {
			OperationID string `json:"operation_id"`
			Timestamp   string `json:"timestamp"`
		}
		if err := json.Unmarshal([]byte(event.Payload), &p); err != nil {
			return target, err
		}
		if event.ID == eventID {
			if event.Kind != "gemini.user.message" || p.OperationID == "" {
				return target, ErrInvalidEditTarget
			}
			operationID = p.OperationID
			found = true
		}
		if !found || event.Kind != "gemini.init" {
			continue
		}
		if p.OperationID == operationID && target.StartedAt == "" {
			target.StartedAt = p.Timestamp
		} else if target.StartedAt != "" && p.OperationID != operationID {
			target.Before = p.Timestamp
			break
		}
	}
	if !found || target.StartedAt == "" {
		return target, ErrInvalidEditTarget
	}
	if _, err := time.Parse(time.RFC3339Nano, target.StartedAt); err != nil {
		return target, ErrInvalidEditTarget
	}
	return target, nil
}

// Commit only after the CLI has loaded the retained prefix successfully. Until
// then, queueing/failure/cancellation leave the original transcript untouched.
func (s *Store) CommitGeminiRewrite(ctx context.Context, threadID, operationID, sessionID string) (protocol.StreamEvent, bool, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.GetContext(ctx, &payload, s.Q("SELECT payload FROM agent_operations WHERE id=? AND kind='gemini.turn.rewrite'"), operationID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	var command protocol.GeminiTurnCommand
	if err := json.Unmarshal([]byte(payload), &command); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if command.ThreadID != threadID || command.Rewrite == nil || sessionID == "" || sessionID == command.SessionID {
		return protocol.StreamEvent{}, false, ErrInvalidEditTarget
	}
	result, err := tx.ExecContext(ctx, s.Q("UPDATE gemini_threads SET session_id=?,updated_at=? WHERE id=? AND operation_id=? AND status IN ('queued','running','interrupting')"), sessionID, time.Now().UTC(), threadID, operationID)
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return protocol.StreamEvent{}, false, ErrThreadActive
	}
	replacementID := operationID + ":user"
	var exists int
	if err := tx.GetContext(ctx, &exists, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND event_id=?"), threadID, replacementID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if exists != 0 {
		return protocol.StreamEvent{}, false, tx.Commit()
	}
	var sequence int64
	if err := tx.GetContext(ctx, &sequence, s.Q("SELECT sequence FROM events WHERE stream_id=? AND event_id=? AND kind='gemini.user.message'"), threadID, command.Rewrite.EventID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrInvalidEditTarget
		}
		return protocol.StreamEvent{}, false, err
	}
	var last int64
	if err := tx.GetContext(ctx, &last, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), threadID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=? AND sequence>=?"), threadID, sequence); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	raw, _ := json.Marshal(map[string]any{"text": command.Prompt, "operation_id": operationID, "history_rewritten": true, "images": command.Images})
	event := protocol.StreamEvent{EventID: replacementID, StreamID: threadID, Sequence: last + 1, Kind: "gemini.user.message", OccurredAt: time.Now().UTC(), Payload: raw}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(raw)); err != nil {
		return event, false, err
	}
	return event, true, tx.Commit()
}
