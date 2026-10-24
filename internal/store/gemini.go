package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type GeminiThread struct {
	ID           string     `db:"id" json:"id"`
	WorkspaceID  string     `db:"workspace_id" json:"workspace_id"`
	SessionID    string     `db:"session_id" json:"session_id"`
	Title        string     `db:"title" json:"title"`
	Model        string     `db:"model" json:"model"`
	ApprovalMode string     `db:"approval_mode" json:"approval_mode"`
	Status       string     `db:"status" json:"status"`
	OperationID  string     `db:"operation_id" json:"operation_id"`
	ArchivedAt   *time.Time `db:"archived_at" json:"archived_at"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
	Path         string     `db:"path" json:"path"`
	ProjectID    string     `db:"project_id" json:"project_id"`
	ProjectName  string     `db:"project_name" json:"project_name"`
	ServerID     string     `db:"server_id" json:"server_id"`
	ServerName   string     `db:"server_name" json:"server_name"`
	ServerStatus string     `db:"server_status" json:"server_status"`
}

const geminiThreadSelect = `SELECT t.*,w.path,w.project_id,p.name AS project_name,w.server_id,s.name AS server_name,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END AS server_status
 FROM gemini_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN projects p ON p.id=w.project_id JOIN servers s ON s.id=w.server_id`

func (s *Store) GeminiThreads(ctx context.Context, archived bool) ([]GeminiThread, error) {
	out := []GeminiThread{}
	condition := "t.archived_at IS NULL"
	if archived {
		condition = "t.archived_at IS NOT NULL"
	}
	err := s.DB.SelectContext(ctx, &out, s.Q(geminiThreadSelect+" WHERE s.revoked_at IS NULL AND "+condition+" ORDER BY t.updated_at DESC LIMIT 200"), time.Now().UTC().Add(-ServerOnlineGracePeriod))
	return out, err
}

func (s *Store) GeminiThread(ctx context.Context, id string) (GeminiThread, error) {
	var thread GeminiThread
	err := s.DB.GetContext(ctx, &thread, s.Q(geminiThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	return thread, err
}

func (s *Store) CreateGeminiThread(ctx context.Context, workspaceID, model, mode string) (GeminiThread, error) {
	id := NewID()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO gemini_threads(id,workspace_id,model,approval_mode,created_at,updated_at)
 SELECT ?,w.id,?,?,?,? FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.id=? AND w.status='ready' AND s.revoked_at IS NULL`), id, model, mode, time.Now().UTC(), time.Now().UTC(), workspaceID)
	if err != nil {
		return GeminiThread{}, err
	}
	return s.GeminiThread(ctx, id)
}

// QueueGeminiTurn claims the session, reserves the workspace and saves the user
// message in one transaction, so concurrent sends cannot create duplicate turns.
func (s *Store) QueueGeminiTurn(ctx context.Context, thread GeminiThread, prompt, model, mode string) (protocol.StreamEvent, error) {
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
	result, err := tx.ExecContext(ctx, s.Q(`UPDATE gemini_threads SET status='queued',operation_id=?,model=?,approval_mode=?,updated_at=?,title=CASE WHEN title='Gemini' THEN ? ELSE title END
 WHERE id=? AND archived_at IS NULL AND status NOT IN ('queued','running','interrupting')`), opID, model, mode, now, truncateGeminiTitle(prompt), thread.ID)
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
	command := protocol.GeminiTurnCommand{ThreadID: thread.ID, Workspace: thread.Path, SessionID: thread.SessionID, Prompt: prompt, Model: model, ApprovalMode: mode}
	payload, err := json.Marshal(command)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at)
 VALUES(?,?,?,?,'gemini.turn.start',?,1,?,?)`), opID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(payload), "gemini:"+opID, now)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	message, _ := json.Marshal(map[string]string{"text": prompt, "operation_id": opID})
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
