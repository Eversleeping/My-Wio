package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
	"time"
)

func (s *Store) PinGeminiThread(ctx context.Context, id string, pinned bool) error {
	var at *time.Time
	if pinned {
		now := time.Now().UTC()
		at = &now
	}
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE gemini_threads SET pinned_at=? WHERE id=?"), at, id)
	return err
}

func (s *Store) ArchiveGeminiProject(ctx context.Context, projectID string, serverOption ...string) (int64, error) {
	scope := ""
	args := []any{time.Now().UTC(), time.Now().UTC(), projectID}
	if len(serverOption) > 0 && serverOption[0] != "" {
		scope = " AND server_id=?"
		args = append(args, serverOption[0])
	}
	r, err := s.DB.ExecContext(ctx, s.Q(`UPDATE gemini_threads SET archived_at=?,updated_at=? WHERE archived_at IS NULL AND status NOT IN ('queued','running','interrupting') AND workspace_id IN (SELECT id FROM workspaces WHERE project_id=?`+scope+`)`), args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// Queue a snapshot while claiming the source thread. Later sends/edits cannot
// move the native boundary between validation and Agent execution.
func (s *Store) QueueGeminiFork(ctx context.Context, thread GeminiThread, eventID string, worktree *protocol.GitWorktreeCreateCommand) (string, string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), thread.ServerID); err != nil {
		return "", "", err
	}
	var updating int
	if err = tx.GetContext(ctx, &updating, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind='agent.update' AND status IN ('queued','delivered','running')"), thread.ServerID); err != nil {
		return "", "", err
	}
	if updating != 0 {
		return "", "", ErrGeminiCredentialsBusy
	}
	opID, targetID := NewID(), NewID()
	r, err := tx.ExecContext(ctx, s.Q(`UPDATE gemini_threads SET operation_id=?,status='queued' WHERE id=? AND session_id<>'' AND archived_at IS NULL AND status NOT IN ('queued','running','interrupting')`), opID, thread.ID)
	if err != nil {
		return "", "", err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return "", "", ErrThreadActive
	}
	var native string
	if err = tx.GetContext(ctx, &native, s.Q("SELECT session_id FROM gemini_threads WHERE id=?"), thread.ID); err != nil {
		return "", "", err
	}
	command := protocol.GeminiForkCommand{SourceThreadID: thread.ID, TargetThreadID: targetID, SessionID: native, SourceWorkspace: thread.Path, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Title: thread.Title + " (continued)"}
	if err = tx.GetContext(ctx, &command.CutoffSequence, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), thread.ID); err != nil {
		return "", "", err
	}
	if eventID != "" {
		// Branch only at the final assistant message of a successful completed turn.
		var events []struct {
			ID       string `db:"event_id"`
			Sequence int64  `db:"sequence"`
			Kind     string `db:"kind"`
			Payload  string `db:"payload"`
		}
		if err = tx.SelectContext(ctx, &events, s.Q("SELECT event_id,sequence,kind,payload FROM events WHERE stream_id=? ORDER BY sequence"), thread.ID); err != nil {
			return "", "", err
		}
		op := ""
		finalAssistant := ""
		completed := false
		found := false
		for _, e := range events {
			var p struct {
				OperationID string `json:"operation_id"`
				Role        string `json:"role"`
				Status      string `json:"status"`
			}
			if json.Unmarshal([]byte(e.Payload), &p) != nil {
				return "", "", ErrInvalidForkTarget
			}
			if e.ID == eventID {
				if e.Kind != "gemini.message" || p.Role != "assistant" || p.OperationID == "" {
					return "", "", ErrInvalidForkTarget
				}
				op = p.OperationID
				found = true
			}
			if !found {
				continue
			}
			if p.OperationID == op {
				if e.Kind == "gemini.message" && p.Role == "assistant" {
					finalAssistant = e.ID
				}
				if e.Kind == "gemini.result" && p.Status == "success" {
					completed = true
					command.CutoffSequence = e.Sequence
				}
			}
			if completed && e.Kind == "gemini.user.message" && p.OperationID != op {
				boundary, e2 := geminiRewriteTarget(ctx, tx, s, thread.ID, e.ID)
				if e2 != nil {
					return "", "", e2
				}
				command.Before = &boundary
				break
			}
		}
		// The UI merges streaming deltas: its event ID can be an earlier chunk of
		// the final contiguous assistant response, but never a tool-intermediate reply.
		if !found || !completed || finalAssistant == "" {
			return "", "", ErrInvalidForkTarget
		}
		toolAfter := false
		found = false
		for _, e := range events {
			if e.ID == eventID {
				toolAfter = false
				found = true
				continue
			}
			if found && e.Sequence <= command.CutoffSequence && (e.Kind == "gemini.tool_use" || e.Kind == "gemini.tool_result") {
				toolAfter = true
			}
		}
		if toolAfter {
			return "", "", ErrInvalidForkTarget
		}
		command.Title = thread.Title + " (branch)"
	}
	kind := "gemini.thread.fork"
	var payload []byte
	if worktree != nil {
		command.WorkspaceID = worktree.TargetWorkspaceID
		command.Workspace = worktree.TargetPath
		worktree.GeminiFork = &command
		payload, err = json.Marshal(worktree)
		kind = "git.worktree.create"
	} else {
		payload, err = json.Marshal(command)
	}
	if err != nil {
		return "", "", err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,?,?,1,?,?)`), opID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, kind, string(payload), "gemini-fork:"+targetID, time.Now().UTC())
	if err != nil {
		return "", "", err
	}
	return opID, targetID, tx.Commit()
}

func (s *Store) commitGeminiFork(ctx context.Context, tx *sqlx.Tx, c protocol.GeminiForkCommand, nativeID string) error {
	if nativeID == "" || nativeID == c.SessionID {
		return errors.New("Gemini fork did not create an independent native session")
	}
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM gemini_threads WHERE id=?"), c.TargetThreadID); err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	now := time.Now().UTC()
	r, err := tx.ExecContext(ctx, s.Q(`INSERT INTO gemini_threads(id,workspace_id,session_id,title,model,approval_mode,thinking_level,status,created_at,updated_at) SELECT ?,?,?,?,model,approval_mode,thinking_level,'idle',?,? FROM gemini_threads WHERE id=? AND session_id=?`), c.TargetThreadID, c.WorkspaceID, nativeID, c.Title, now, now, c.SourceThreadID, c.SessionID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err = tx.SelectContext(ctx, &events, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence<=? ORDER BY sequence"), c.SourceThreadID, c.CutoffSequence); err != nil {
		return err
	}
	for _, e := range events {
		if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), c.TargetThreadID, e.Sequence, e.Kind, e.OccurredAt, e.Payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CommitGeminiFork(ctx context.Context, c protocol.GeminiForkCommand, nativeID string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.commitGeminiFork(ctx, tx, c, nativeID); err != nil {
		return err
	}
	return tx.Commit()
}
