package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrGeminiSkillsBusy = errors.New("finish active Gemini tasks and configuration changes on this server before managing skills")

func (s *Store) QueueGeminiSkillManagement(ctx context.Context, workspace Workspace, command protocol.GeminiSkillManageCommand, requestID string) (string, error) {
	command.Workspace = workspace.Path
	if err := protocol.ValidateGeminiSkillManagement(command); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(command)
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), workspace.ServerID); err != nil {
		return "", err
	}
	key := "gemini-skills:" + workspace.ID + ":" + requestID
	var existing Operation
	err = tx.GetContext(ctx, &existing, s.Q(operationSelect+" WHERE idempotency_key=?"), key)
	if err == nil {
		if existing.Payload != string(raw) {
			return "", stateConflict("skill request already has different content")
		}
		return existing.ID, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var active int
	if err = tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('gemini.turn.start','gemini.turn.rewrite','gemini.thread.fork','git.worktree.create','gemini.skills.list','gemini.skills.manage','gemini.credentials.configure','cli.install','agent.update') AND status IN ('queued','delivered','running')`), workspace.ServerID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrGeminiSkillsBusy
	}
	if err = tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM gemini_threads t JOIN workspaces w ON w.id=t.workspace_id WHERE w.server_id=? AND t.status IN ('queued','running','interrupting')`), workspace.ServerID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrGeminiSkillsBusy
	}
	id := NewID()
	if busy, err := s.collaborationWorkspaceBusy(ctx, tx, workspace.ID); err != nil {
		return "", err
	} else if busy {
		return "", ErrWorkspaceWriteActive
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,'gemini.skills.manage',?,1,?,?)`), id, workspace.ServerID, workspace.ProjectID, workspace.ID, string(raw), key, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
