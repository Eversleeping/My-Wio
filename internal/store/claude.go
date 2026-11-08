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
	QuestionsSupported bool       `db:"-" json:"questions_supported"`
	RewriteSupported   bool       `db:"-" json:"rewrite_supported"`
	SteerSupported     bool       `db:"-" json:"steer_supported"`
	SteerReady         bool       `db:"-" json:"steer_ready"`
	ClaudeVersion      string     `db:"claude_version" json:"-"`
	SteerState         string     `db:"steer_state" json:"-"`
	SkillsSupported    bool       `db:"-" json:"skills_supported"`
	ID                 string     `db:"id" json:"id"`
	WorkspaceID        string     `db:"workspace_id" json:"workspace_id"`
	SessionID          string     `db:"session_id" json:"session_id"`
	Title              string     `db:"title" json:"title"`
	Model              string     `db:"model" json:"model"`
	PermissionMode     string     `db:"permission_mode" json:"permission_mode"`
	Effort             string     `db:"effort" json:"effort"`
	EffortSupported    bool       `json:"effort_supported"`
	InputSupported     bool       `json:"input_supported"`
	PinnedAt           *time.Time `db:"pinned_at" json:"pinned_at"`
	ForkSnapshotID     string     `db:"fork_snapshot_id" json:"-"`
	ForkSessionID      string     `db:"fork_session_id" json:"-"`
	ProjectPinnedAt    *time.Time `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt    *time.Time `db:"project_hidden_at" json:"project_hidden_at"`
	Status             string     `db:"status" json:"status"`
	OperationID        string     `db:"operation_id" json:"operation_id"`
	ArchivedAt         *time.Time `db:"archived_at" json:"archived_at"`
	CreatedAt          time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time  `db:"updated_at" json:"updated_at"`
	Path               string     `db:"path" json:"path"`
	ProjectID          string     `db:"project_id" json:"project_id"`
	ProjectName        string     `db:"project_name" json:"project_name"`
	ServerID           string     `db:"server_id" json:"server_id"`
	ServerName         string     `db:"server_name" json:"server_name"`
	ServerStatus       string     `db:"server_status" json:"server_status"`
	AgentVersion       string     `db:"agent_version" json:"-"`
	RecoveryPending    bool       `db:"recovery_pending" json:"recovery_pending"`
}

const claudeThreadSelect = `SELECT t.*,cm.pinned_at,COALESCE(cm.fork_snapshot_id,'') AS fork_snapshot_id,COALESCE(cm.fork_session_id,'') AS fork_session_id,COALESCE(cto.effort,'') AS effort,w.path,w.project_id,` + projectServerName + ` AS project_name,` + projectServerPin + ` AS project_pinned_at,` + projectServerHidden + ` AS project_hidden_at,
 w.server_id,s.name AS server_name,s.agent_version,COALESCE(ccli.version,'') AS claude_version,
 COALESCE((SELECT payload FROM events WHERE stream_id=t.id AND kind IN ('claude.steer.ready','claude.steer.closed') ORDER BY sequence DESC LIMIT 1),'') AS steer_state,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END AS server_status,
 CASE WHEN t.status='interrupted' AND EXISTS (SELECT 1 FROM agent_operations o WHERE o.id=t.operation_id AND o.status IN ('queued','delivered','running')) THEN 1 ELSE 0 END AS recovery_pending
 FROM claude_threads t LEFT JOIN claude_thread_management cm ON cm.thread_id=t.id LEFT JOIN claude_thread_options cto ON cto.thread_id=t.id JOIN workspaces w ON w.id=t.workspace_id JOIN projects p ON p.id=w.project_id JOIN servers s ON s.id=w.server_id LEFT JOIN server_claude_cli ccli ON ccli.server_id=s.id` + projectServerJoin

func (s *Store) ClaudeThreads(ctx context.Context, archived bool) ([]ClaudeThread, error) {
	condition := "t.archived_at IS NULL"
	if archived {
		condition = "t.archived_at IS NOT NULL"
	}
	threads := []ClaudeThread{}
	err := s.DB.SelectContext(ctx, &threads, s.Q(claudeThreadSelect+" WHERE s.revoked_at IS NULL AND "+condition+" ORDER BY CASE WHEN cm.pinned_at IS NULL THEN 1 ELSE 0 END,cm.pinned_at DESC,t.updated_at DESC LIMIT 200"), time.Now().UTC().Add(-ServerOnlineGracePeriod))
	for i := range threads {
		threads[i].RewriteSupported = buildinfo.SupportsClaudeRewrite(threads[i].AgentVersion)
		threads[i].setSteerCapability()
		threads[i].QuestionsSupported = buildinfo.SupportsClaudeQuestions(threads[i].AgentVersion, threads[i].ClaudeVersion)
		threads[i].EffortSupported = buildinfo.SupportsClaudeEffort(threads[i].AgentVersion)
		threads[i].SkillsSupported = buildinfo.SupportsClaudeSkills(threads[i].AgentVersion)
		threads[i].InputSupported = buildinfo.SupportsClaudeInput(threads[i].AgentVersion)
	}
	return threads, err
}

func (s *Store) ClaudeThread(ctx context.Context, id string) (ClaudeThread, error) {
	var thread ClaudeThread
	err := s.DB.GetContext(ctx, &thread, s.Q(claudeThreadSelect+" WHERE t.id=? AND s.revoked_at IS NULL"), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	thread.setSteerCapability()
	thread.QuestionsSupported = buildinfo.SupportsClaudeQuestions(thread.AgentVersion, thread.ClaudeVersion)
	thread.RewriteSupported = buildinfo.SupportsClaudeRewrite(thread.AgentVersion)
	thread.EffortSupported = buildinfo.SupportsClaudeEffort(thread.AgentVersion)
	thread.SkillsSupported = buildinfo.SupportsClaudeSkills(thread.AgentVersion)
	thread.InputSupported = buildinfo.SupportsClaudeInput(thread.AgentVersion)
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
func (s *Store) QueueClaudeTurn(ctx context.Context, thread ClaudeThread, prompt, model, mode, effort string, images ...protocol.TurnImage) (protocol.StreamEvent, error) {
	return s.queueClaudeInput(ctx, thread, "", prompt, model, mode, effort, nil, images...)
}

// QueueScheduledClaudeTurn queues a scheduled task's turn and records the run
// as queued on the task in the same transaction. It returns the operation ID.
func (s *Store) QueueScheduledClaudeTurn(ctx context.Context, taskID string, thread ClaudeThread, prompt, model, mode, effort string, at time.Time) (string, error) {
	run := &scheduledRun{table: "claude_scheduled_tasks", taskID: taskID, at: at}
	if _, err := s.queueClaudeInput(ctx, thread, "", prompt, model, mode, effort, run); err != nil {
		return "", err
	}
	return run.operationID, nil
}

func (s *Store) QueueClaudeRewrite(ctx context.Context, thread ClaudeThread, eventID, prompt, model, mode, effort string, images ...protocol.TurnImage) (protocol.StreamEvent, error) {
	return s.queueClaudeInput(ctx, thread, eventID, prompt, model, mode, effort, nil, images...)
}

func (s *Store) queueClaudeInput(ctx context.Context, thread ClaudeThread, editID, prompt, model, mode, effort string, run *scheduledRun, images ...protocol.TurnImage) (protocol.StreamEvent, error) {
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
	if len(title) == 0 {
		title = []rune("Image")
	}
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
	command.Steer = thread.SteerSupported
	command.Questions = thread.QuestionsSupported
	command.Images = images
	if editID != "" {
		target, err := s.claudeRewriteTarget(ctx, tx, thread.ID, editID)
		if err != nil || sessionID == "" {
			return protocol.StreamEvent{}, ErrInvalidEditTarget
		}
		command.Rewrite = &target
	}
	if sessionID == "" {
		command.ForkSnapshotID = thread.ForkSnapshotID
		command.ForkSessionID = thread.ForkSessionID
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return protocol.StreamEvent{}, err
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,'claude.turn.start',?,1,?,?)`), opID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(payload), "claude:"+opID, now)
	if err != nil {
		if isWorkspaceWriteConflict(err) {
			return protocol.StreamEvent{}, ErrWorkspaceWriteActive
		}
		return protocol.StreamEvent{}, err
	}
	if err := s.recordScheduledRun(ctx, tx, run, opID); err != nil {
		return protocol.StreamEvent{}, err
	}
	if editID != "" {
		return protocol.StreamEvent{}, tx.Commit()
	}
	raw, _ := json.Marshal(map[string]any{"text": prompt, "images": images, "operation_id": opID})
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
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE claude_scheduled_tasks SET last_run_status='cancelled',last_run_message='Cancelled before dispatch',updated_at=? WHERE last_operation_id=? AND last_run_status IN ('queued','running')"), time.Now().UTC(), thread.OperationID); err != nil {
			return err
		}
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
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE claude_scheduled_tasks SET last_run_status='cancelled',last_run_message='Process exit confirmed by user',updated_at=? WHERE last_operation_id=? AND last_run_status IN ('queued','running')"), time.Now().UTC(), operationID); err != nil {
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

func (thread *ClaudeThread) setSteerCapability() {
	thread.SteerSupported = buildinfo.SupportsClaudeSteering(thread.AgentVersion, thread.ClaudeVersion)
	var state struct {
		OperationID string `json:"operation_id"`
		Ready       bool   `json:"ready"`
	}
	thread.SteerReady = thread.SteerSupported && thread.Status == "running" && json.Unmarshal([]byte(thread.SteerState), &state) == nil && state.OperationID == thread.OperationID && state.Ready
}
