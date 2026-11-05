package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/protocol"
)

type ClaudeThread struct {
	ID              string     `db:"id" json:"id"`
	WorkspaceID     string     `db:"workspace_id" json:"workspace_id"`
	SessionID       string     `db:"session_id" json:"session_id"`
	Title           string     `db:"title" json:"title"`
	Model           string     `db:"model" json:"model"`
	PermissionMode  string     `db:"permission_mode" json:"permission_mode"`
	Effort          string     `db:"effort" json:"effort"`
	EffortSupported bool       `json:"effort_supported"`
	ProjectPinnedAt *time.Time `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt *time.Time `db:"project_hidden_at" json:"project_hidden_at"`
	Status          string     `db:"status" json:"status"`
	OperationID     string     `db:"operation_id" json:"operation_id"`
	ArchivedAt      *time.Time `db:"archived_at" json:"archived_at"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
	Path            string     `db:"path" json:"path"`
	ProjectID       string     `db:"project_id" json:"project_id"`
	ProjectName     string     `db:"project_name" json:"project_name"`
	ServerID        string     `db:"server_id" json:"server_id"`
	ServerName      string     `db:"server_name" json:"server_name"`
	ServerStatus    string     `db:"server_status" json:"server_status"`
	AgentVersion    string     `db:"agent_version" json:"-"`
	RecoveryPending bool       `db:"recovery_pending" json:"recovery_pending"`
}

const claudeThreadSelect = `SELECT t.*,COALESCE(cto.effort,'') AS effort,w.path,w.project_id,` + projectServerName + ` AS project_name,` + projectServerPin + ` AS project_pinned_at,` + projectServerHidden + ` AS project_hidden_at,
 w.server_id,s.name AS server_name,s.agent_version,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END AS server_status,
 CASE WHEN t.status='interrupted' AND EXISTS (SELECT 1 FROM agent_operations o WHERE o.id=t.operation_id AND o.status IN ('queued','delivered','running')) THEN 1 ELSE 0 END AS recovery_pending
 FROM claude_threads t LEFT JOIN claude_thread_options cto ON cto.thread_id=t.id JOIN workspaces w ON w.id=t.workspace_id JOIN projects p ON p.id=w.project_id JOIN servers s ON s.id=w.server_id` + projectServerJoin

func (s *Store) ClaudeThreads(ctx context.Context, archived bool) ([]ClaudeThread, error) {
	condition := "t.archived_at IS NULL"
	if archived {
		condition = "t.archived_at IS NOT NULL"
	}
	threads := []ClaudeThread{}
	err := s.DB.SelectContext(ctx, &threads, s.Q(claudeThreadSelect+" WHERE s.revoked_at IS NULL AND "+condition+" ORDER BY t.updated_at DESC LIMIT 200"), time.Now().UTC().Add(-ServerOnlineGracePeriod))
	for i := range threads {
		threads[i].EffortSupported = buildinfo.SupportsClaudeEffort(threads[i].AgentVersion)
	}
	return threads, err
}

func (s *Store) ClaudeThread(ctx context.Context, id string) (ClaudeThread, error) {
	var thread ClaudeThread
	err := s.DB.GetContext(ctx, &thread, s.Q(claudeThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	thread.EffortSupported = buildinfo.SupportsClaudeEffort(thread.AgentVersion)
	return thread, err
}

func (s *Store) CreateClaudeThread(ctx context.Context, workspaceID, model, mode, effort string) (ClaudeThread, error) {
	id, now := NewID(), time.Now().UTC()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return ClaudeThread{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q(`INSERT INTO claude_threads(id,workspace_id,model,permission_mode,created_at,updated_at)
 SELECT ?,w.id,?,?,?,? FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), id, model, mode, now, now, workspaceID)
	if err != nil {
		return ClaudeThread{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return ClaudeThread{}, err
		}
		return ClaudeThread{}, sql.ErrNoRows
	}
	if err := s.showProjectForNewThread(ctx, tx, workspaceID); err != nil {
		return ClaudeThread{}, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_options(thread_id,effort) VALUES(?,?)"), id, effort); err != nil {
		return ClaudeThread{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClaudeThread{}, err
	}
	return s.ClaudeThread(ctx, id)
}

// QueueClaudeTurn atomically claims the thread and the shared workspace write lock.
func (s *Store) QueueClaudeTurn(ctx context.Context, thread ClaudeThread, prompt, model, mode, effort string) (protocol.StreamEvent, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	defer tx.Rollback()
	// Serialize a new turn with credential changes and CLI installation.
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), thread.ServerID); err != nil {
		return protocol.StreamEvent{}, err
	}
	var configuring int
	if err := tx.GetContext(ctx, &configuring, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('claude.credentials.configure','cli.install','agent.update') AND status IN ('queued','delivered','running')"), thread.ServerID); err != nil {
		return protocol.StreamEvent{}, err
	}
	if configuring != 0 {
		return protocol.StreamEvent{}, ErrClaudeCredentialsBusy
	}
	locked, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=? AND status='ready'"), thread.WorkspaceID)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	if n, err := locked.RowsAffected(); err != nil || n != 1 {
		return protocol.StreamEvent{}, ErrThreadActive
	}
	opID, now := NewID(), time.Now().UTC()
	title := []rune(prompt)
	if len(title) > 60 {
		title = title[:60]
	}
	result, err := tx.ExecContext(ctx, s.Q(`UPDATE claude_threads SET status='queued',operation_id=?,model=?,permission_mode=?,updated_at=?,title=CASE WHEN title='Claude' THEN ? ELSE title END
 WHERE id=? AND archived_at IS NULL AND status NOT IN ('queued','running','interrupting') AND (status<>'interrupted' OR NOT EXISTS (SELECT 1 FROM agent_operations WHERE id=claude_threads.operation_id AND status IN ('queued','delivered','running')))`), opID, model, mode, now, string(title), thread.ID)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return protocol.StreamEvent{}, ErrThreadActive
	}
	var sessionID string
	if err := tx.GetContext(ctx, &sessionID, s.Q("SELECT session_id FROM claude_threads WHERE id=?"), thread.ID); err != nil {
		return protocol.StreamEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_options(thread_id,effort) VALUES(?,?) ON CONFLICT(thread_id) DO UPDATE SET effort=excluded.effort"), thread.ID, effort); err != nil {
		return protocol.StreamEvent{}, err
	}
	command := protocol.ClaudeTurnCommand{ThreadID: thread.ID, Workspace: thread.Path, SessionID: sessionID, Prompt: prompt, Model: model, PermissionMode: mode, Effort: effort}
	payload, err := json.Marshal(command)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,'claude.turn.start',?,1,?,?)`), opID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(payload), "claude:"+opID, now)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	raw, _ := json.Marshal(map[string]string{"text": prompt, "operation_id": opID})
	event := protocol.StreamEvent{EventID: NewID(), StreamID: thread.ID, Kind: "claude.user.message", OccurredAt: now, Payload: raw}
	if err := tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), thread.ID); err != nil {
		return event, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, now, string(raw)); err != nil {
		return event, err
	}
	return event, tx.Commit()
}

func (s *Store) InterruptClaudeTurn(ctx context.Context, thread ClaudeThread) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=status WHERE id=? AND kind='claude.turn.start' AND status IN ('queued','delivered','running')"), thread.OperationID)
	if err != nil {
		return err
	}
	if n, err := locked.RowsAffected(); err != nil || n != 1 {
		return ErrThreadActive
	}
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
		status = "interrupted"
	}
	result, err = tx.ExecContext(ctx, s.Q("UPDATE claude_threads SET status=?,updated_at=? WHERE id=? AND operation_id=? AND (status IN ('queued','running') OR (status='interrupted' AND EXISTS (SELECT 1 FROM agent_operations WHERE id=claude_threads.operation_id AND status IN ('delivered','running'))))"), status, time.Now().UTC(), thread.ID, thread.OperationID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrThreadActive
	}
	if count == 0 {
		payload, _ := json.Marshal(protocol.ClaudeInterruptCommand{ThreadID: thread.ID, OperationID: thread.OperationID, RecoveryPending: thread.RecoveryPending})
		id := NewID()
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,'claude.turn.interrupt',?,?,?)`), id, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(payload), "claude-interrupt:"+id, time.Now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ConfirmClaudeStopped(ctx context.Context, threadID, operationID string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=status WHERE id=? AND kind='claude.turn.start' AND status IN ('delivered','running')"), operationID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrThreadActive
	}
	result, err = tx.ExecContext(ctx, s.Q("UPDATE claude_threads SET updated_at=? WHERE id=? AND operation_id=? AND status='interrupted'"), time.Now().UTC(), threadID, operationID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrThreadActive
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='cancelled',result=?,completed_at=? WHERE id=?"), "Process exit confirmed by user", time.Now().UTC(), operationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateClaudeThread(ctx context.Context, id, title string, archived bool) error {
	var at *time.Time
	if archived {
		now := time.Now().UTC()
		at = &now
	}
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE claude_threads SET title=?,archived_at=?,updated_at=? WHERE id=? AND status NOT IN ('queued','running','interrupting') AND (status<>'interrupted' OR NOT EXISTS (SELECT 1 FROM agent_operations WHERE id=claude_threads.operation_id AND status IN ('queued','delivered','running')))"), title, at, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrThreadActive
	}
	return nil
}

func (s *Store) DeleteClaudeThread(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("DELETE FROM claude_threads WHERE id=? AND status NOT IN ('queued','running','interrupting') AND (status<>'interrupted' OR NOT EXISTS (SELECT 1 FROM agent_operations WHERE id=claude_threads.operation_id AND status IN ('queued','delivered','running')))"), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrThreadActive
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=?"), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedClaudeOperations(ctx context.Context, serverID string) ([]string, error) {
	var ids []string
	err := s.DB.SelectContext(ctx, &ids, s.Q("SELECT id FROM agent_operations WHERE server_id=? AND kind='claude.turn.start' AND (status='running' OR (status='delivered' AND delivered_at<?)) ORDER BY created_at"), serverID, time.Now().UTC().Add(-30*time.Second))
	return ids, err
}
