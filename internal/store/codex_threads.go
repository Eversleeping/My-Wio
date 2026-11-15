package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessiontools"
)

func (s *Store) LatestActiveTurnID(ctx context.Context, threadID string) (string, error) {
	return s.latestActiveTurnID(ctx, s.DB, threadID)
}

func (s *Store) latestActiveTurnID(ctx context.Context, db sqlx.QueryerContext, threadID string) (string, error) {
	var event struct {
		Kind    string `db:"kind"`
		Payload string `db:"payload"`
	}
	if err := sqlx.GetContext(ctx, db, &event, s.Q("SELECT kind,payload FROM events WHERE stream_id=? AND kind IN ('turn.accepted','codex.turn.started','codex.turn.completed','codex.turn.failed','codex.turn.cancelled') ORDER BY sequence DESC LIMIT 1"), threadID); err != nil {
		return "", err
	}
	switch event.Kind {
	case protocol.EventCodexTurnCompleted, protocol.EventCodexTurnFailed, protocol.EventCodexTurnCancelled:
		return "", nil
	}
	var value struct {
		TurnID string `json:"turn_id"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal([]byte(event.Payload), &value); err != nil {
		return "", err
	}
	if event.Kind == protocol.EventCodexTurnStarted {
		return value.Turn.ID, nil
	}
	return value.TurnID, nil
}

func (s *Store) RewriteThread(ctx context.Context, thread Thread, editEventID string, command protocol.StartTurnCommand, userPayload json.RawMessage) (string, protocol.StreamEvent, error) {
	var operationID string
	var replacement protocol.StreamEvent
	err := s.inTx(ctx, s.addEventTxOptions(), func(tx *sqlx.Tx) error {
		if err := s.lockCodexRequestThread(ctx, tx, thread.ID); err != nil {
			return err
		}
		if s.isPostgres() {
			if err := lockEventStream(ctx, tx, thread.ID); err != nil {
				return err
			}
		}
		var status string
		if err := tx.GetContext(ctx, &status, s.Q("SELECT status FROM codex_threads WHERE id=?"), thread.ID); err != nil {
			return err
		}
		if status == "queued" || status == "running" {
			return ErrThreadActive
		}
		var target struct {
			Sequence int64  `db:"sequence"`
			Kind     string `db:"kind"`
			Payload  string `db:"payload"`
		}
		if err := tx.GetContext(ctx, &target, s.Q("SELECT sequence,kind,payload FROM events WHERE stream_id=? AND event_id=?"), thread.ID, editEventID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidEditTarget
			}
			return err
		}
		if target.Kind != protocol.EventUserMessage {
			return ErrInvalidEditTarget
		}
		var metadata struct {
			Steer bool `json:"steer"`
		}
		if err := json.Unmarshal([]byte(target.Payload), &metadata); err != nil || metadata.Steer {
			return ErrInvalidEditTarget
		}
		var numTurns uint32
		if err := tx.GetContext(ctx, &numTurns, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND kind='turn.accepted' AND sequence>?"), thread.ID, target.Sequence); err != nil {
			return err
		}
		var cutoffSequence int64
		if err := tx.GetContext(ctx, &cutoffSequence, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), thread.ID); err != nil {
			return err
		}
		operationID = NewID()
		var message map[string]json.RawMessage
		if err := json.Unmarshal(userPayload, &message); err != nil {
			return err
		}
		if message == nil {
			return ErrInvalidEditTarget
		}
		message["operation_id"], _ = json.Marshal(operationID)
		associatedPayload, err := json.Marshal(message)
		if err != nil {
			return err
		}
		userPayload = associatedPayload
		replacement = protocol.StreamEvent{EventID: NewID(), StreamID: thread.ID, Sequence: target.Sequence, Kind: protocol.EventUserMessage, OccurredAt: time.Now().UTC(), Payload: userPayload}
		rewrite := protocol.RewriteTurnCommand{
			Start:              command,
			NumTurns:           numTurns,
			EditEventID:        editEventID,
			ReplacementEventID: replacement.EventID,
			ReplacementPayload: userPayload,
			CutoffSequence:     cutoffSequence,
		}
		operationPayload, err := json.Marshal(rewrite)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,project_id,workspace_id,thread_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,?)"), operationID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, thread.ID, protocol.OpCodexTurnRewrite, string(operationPayload), "codex-rewrite:"+NewID(), now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET status='queued',updated_at=? WHERE id=?"), now, thread.ID)
		return err
	})
	if err != nil {
		return "", protocol.StreamEvent{}, err
	}
	return operationID, replacement, nil
}

func (s *Store) CommitThreadRewrite(ctx context.Context, threadID, codexThreadID, editEventID, replacementEventID string, replacementPayload json.RawMessage, cutoffSequence int64) (protocol.StreamEvent, bool, error) {
	var event protocol.StreamEvent
	var applied bool
	err := s.inTx(ctx, s.addEventTxOptions(), func(tx *sqlx.Tx) error {
		if err := s.lockCodexRequestThread(ctx, tx, threadID); err != nil {
			return err
		}
		if s.isPostgres() {
			if err := lockEventStream(ctx, tx, threadID); err != nil {
				return err
			}
		}
		var target struct {
			Sequence int64  `db:"sequence"`
			Kind     string `db:"kind"`
		}
		if err := tx.GetContext(ctx, &target, s.Q("SELECT sequence,kind FROM events WHERE stream_id=? AND event_id=?"), threadID, editEventID); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var existing struct {
				Sequence   int64     `db:"sequence"`
				Kind       string    `db:"kind"`
				OccurredAt time.Time `db:"occurred_at"`
				Payload    string    `db:"payload"`
			}
			if err := tx.GetContext(ctx, &existing, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND event_id=?"), threadID, replacementEventID); err != nil {
				return err
			}
			event = protocol.StreamEvent{EventID: replacementEventID, StreamID: threadID, Sequence: existing.Sequence, Kind: existing.Kind, OccurredAt: existing.OccurredAt, Payload: json.RawMessage(existing.Payload)}
			applied = false
			return nil
		}
		if target.Kind != protocol.EventUserMessage {
			return ErrInvalidEditTarget
		}
		if cutoffSequence < target.Sequence {
			return ErrInvalidEditTarget
		}
		// Commands queued before operation_id was included can be acknowledged
		// after an upgrade. Recover their association from the stored command;
		// truly legacy events without a matching operation remain valid JSON.
		var metadata struct {
			OperationID string `json:"operation_id"`
		}
		if err := json.Unmarshal(replacementPayload, &metadata); err != nil {
			return err
		}
		if metadata.OperationID == "" {
			var candidates []struct {
				ID      string `db:"id"`
				Payload string `db:"payload"`
			}
			if err := tx.SelectContext(ctx, &candidates, s.Q("SELECT id,payload FROM agent_operations WHERE thread_id=? AND kind='codex.turn.rewrite' AND payload LIKE ?"), threadID, "%"+replacementEventID+"%"); err != nil {
				return err
			}
			for _, candidate := range candidates {
				var rewrite protocol.RewriteTurnCommand
				if json.Unmarshal([]byte(candidate.Payload), &rewrite) != nil || rewrite.ReplacementEventID != replacementEventID {
					continue
				}
				var message map[string]json.RawMessage
				if err := json.Unmarshal(replacementPayload, &message); err != nil {
					return err
				}
				if message != nil {
					message["operation_id"], _ = json.Marshal(candidate.ID)
					var err error
					replacementPayload, err = json.Marshal(message)
					if err != nil {
						return err
					}
				}
				break
			}
		}
		var earlierMessages int
		if err := tx.GetContext(ctx, &earlierMessages, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND kind='user.message' AND sequence<?"), threadID, target.Sequence); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=? AND sequence>=? AND sequence<=?"), threadID, target.Sequence, cutoffSequence); err != nil {
			return err
		}
		now := time.Now().UTC()
		event = protocol.StreamEvent{EventID: replacementEventID, StreamID: threadID, Sequence: target.Sequence, Kind: protocol.EventUserMessage, OccurredAt: now, Payload: replacementPayload}
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(event.Payload)); err != nil {
			return err
		}
		title := ""
		if earlierMessages == 0 {
			title = "New session"
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET codex_thread_id=?,status='running',title=CASE WHEN ?='' THEN title ELSE ? END,updated_at=? WHERE id=?"), codexThreadID, title, title, now, threadID); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	return event, applied, nil
}

var ErrThreadActive = errors.New("Codex session is active")

var ErrInvalidEditTarget = errors.New("invalid Codex message edit target")

type Thread struct {
	Capabilities    session.Capabilities `db:"-" json:"capabilities"`
	AgentVersion    string               `db:"agent_version" json:"-"`
	CLIVersion      string               `db:"codex_version" json:"-"`
	ID              string               `db:"id" json:"id"`
	WorkspaceID     string               `db:"workspace_id" json:"workspace_id"`
	ProjectID       string               `db:"project_id" json:"project_id"`
	CodexThreadID   string               `db:"codex_thread_id" json:"codex_thread_id"`
	Title           string               `db:"title" json:"title"`
	Status          string               `db:"status" json:"status"`
	Path            string               `db:"path" json:"path"`
	ServerID        string               `db:"server_id" json:"server_id"`
	ServerName      string               `db:"server_name" json:"server_name"`
	ProjectName     string               `db:"project_name" json:"project_name"`
	PinnedAt        *time.Time           `db:"pinned_at" json:"pinned_at"`
	ArchivedAt      *time.Time           `db:"archived_at" json:"archived_at"`
	ProjectPinnedAt *time.Time           `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt *time.Time           `db:"project_hidden_at" json:"project_hidden_at"`
	CreatedAt       time.Time            `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time            `db:"updated_at" json:"updated_at"`
}

// ThreadPage is a bounded window of Codex sessions. The API layer turns this
// into its public pagination response and owns the requested offset.
type ThreadPage struct {
	Items   []Thread
	HasMore bool
}

const codexThreadSelect = `SELECT s.agent_version,` + codexEngineVersion + ` AS codex_version,t.id,t.workspace_id,w.project_id,t.codex_thread_id,t.title,t.status,w.path,w.server_id,s.name server_name,` + projectServerName + ` project_name,t.pinned_at,t.archived_at,` + projectServerPin + ` project_pinned_at,` + projectServerHidden + ` project_hidden_at,t.created_at,t.updated_at FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id` + projectServerJoin

func (s *Store) ListThreads(ctx context.Context, archivedOption ...string) ([]Thread, error) {
	var out []Thread
	archived := ""
	if len(archivedOption) > 0 {
		archived = archivedOption[0]
	}
	filter := " WHERE t.archived_at IS NULL"
	if archived == "true" {
		filter = " WHERE t.archived_at IS NOT NULL"
	} else if archived == "all" {
		filter = ""
	}
	err := s.DB.SelectContext(ctx, &out, codexThreadSelect+filter+` ORDER BY CASE WHEN (`+projectServerPin+`) IS NULL THEN 1 ELSE 0 END,project_pinned_at DESC,CASE WHEN t.pinned_at IS NULL THEN 1 ELSE 0 END,t.pinned_at DESC,t.updated_at DESC`)
	for i := range out {
		out[i].setCapabilities()
	}
	return out, err
}

// ListThreadsPage returns one ordered session window and one extra row is
// read to let callers determine whether another window exists. It deliberately
// leaves ListThreads unchanged for clients that still need the complete list.
func (s *Store) ListThreadsPage(ctx context.Context, archived string, limit, offset int) (ThreadPage, error) {
	if limit <= 0 {
		return ThreadPage{}, errors.New("thread page limit must be positive")
	}
	if offset < 0 {
		return ThreadPage{}, errors.New("thread page offset must not be negative")
	}
	filter := " WHERE t.archived_at IS NULL"
	if archived == "true" {
		filter = " WHERE t.archived_at IS NOT NULL"
	} else if archived == "all" {
		filter = ""
	}
	items := make([]Thread, 0, limit+1)
	err := s.DB.SelectContext(ctx, &items, s.Q(codexThreadSelect+filter+` ORDER BY CASE WHEN (`+projectServerPin+`) IS NULL THEN 1 ELSE 0 END,project_pinned_at DESC,CASE WHEN t.pinned_at IS NULL THEN 1 ELSE 0 END,t.pinned_at DESC,t.updated_at DESC,t.id DESC LIMIT ? OFFSET ?`), limit+1, offset)
	if err != nil {
		return ThreadPage{}, err
	}
	for i := range items {
		items[i].setCapabilities()
	}
	page := ThreadPage{Items: items}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
	}
	return page, nil
}

func (s *Store) Thread(ctx context.Context, id string) (Thread, error) {
	var out Thread
	err := s.DB.GetContext(ctx, &out, s.Q(codexThreadSelect+` WHERE t.id=?`), id)
	out.setCapabilities()
	return out, err
}

func (s *Store) CreateThread(ctx context.Context, workspaceID, title string) (Thread, error) {
	if title == "" {
		title = "New session"
	}
	id := NewID()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback()
	if err := s.createCollaborationSessionTx(ctx, tx, sessiontools.Ref{Engine: "codex", ID: id}, workspaceID, title, "", "on-request", ""); err != nil {
		return Thread{}, err
	}
	if err := tx.Commit(); err != nil {
		return Thread{}, err
	}
	return s.Thread(ctx, id)
}

func (s *Store) DeleteThread(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("DELETE FROM codex_threads WHERE id=? AND status NOT IN ('queued','running')"), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var count int
		if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=?"), id); err != nil {
			return err
		}
		if count == 0 {
			return sql.ErrNoRows
		}
		return ErrThreadActive
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM scheduled_tasks WHERE engine='codex' AND thread_id=?"), id); err != nil {
		return err
	}
	key := "codex:" + id
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM session_subscriptions WHERE target_key=? OR source_key=?"), key, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM session_links WHERE target_key=?"), key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=?"), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetThreadStatus(ctx context.Context, id, status string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET status=?,updated_at=? WHERE id=?"), status, time.Now().UTC(), id)
	return err
}

func (s *Store) ClaimThreadForTurn(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET status='queued',updated_at=? WHERE id=? AND status NOT IN ('queued','running')"), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrThreadActive
	}
	return nil
}

func (s *Store) SetThreadTitle(ctx context.Context, id, title string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET title=?,updated_at=? WHERE id=?"), title, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateThread(ctx context.Context, id string, title *string, pinned *bool, archivedOption ...*bool) (Thread, error) {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if title != nil {
		sets = append(sets, "title=?")
		args = append(args, *title)
	}
	now := time.Now().UTC()
	if pinned != nil {
		sets = append(sets, "pinned_at=?")
		if *pinned {
			args = append(args, now)
		} else {
			args = append(args, nil)
		}
	}
	var archived *bool
	if len(archivedOption) > 0 {
		archived = archivedOption[0]
	}
	if archived != nil {
		sets = append(sets, "archived_at=?")
		if *archived {
			args = append(args, now)
		} else {
			args = append(args, nil)
		}
	}
	if len(sets) == 0 {
		return s.Thread(ctx, id)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, now, id)
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET "+strings.Join(sets, ",")+" WHERE id=?"), args...)
	if err != nil {
		return Thread{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Thread{}, err
	}
	if rows == 0 {
		return Thread{}, sql.ErrNoRows
	}
	return s.Thread(ctx, id)
}

func (s *Store) ArchiveProjectThreads(ctx context.Context, projectID string, serverOption ...string) (int64, error) {
	scope := ""
	args := []any{time.Now().UTC(), time.Now().UTC(), projectID}
	if len(serverOption) > 0 && serverOption[0] != "" {
		scope = " AND server_id=?"
		args = append(args, serverOption[0])
	}
	result, err := s.DB.ExecContext(ctx, s.Q(`UPDATE codex_threads SET archived_at=?,updated_at=? WHERE archived_at IS NULL AND workspace_id IN (SELECT id FROM workspaces WHERE project_id=?`+scope+`) AND status NOT IN ('queued','running')`), args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) CommitThreadFork(ctx context.Context, command protocol.ForkThreadCommand, codexThread string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.GetContext(ctx, &existing, s.Q("SELECT codex_thread_id FROM codex_threads WHERE id=?"), command.TargetThreadID)
	if err == nil {
		if existing == codexThread {
			return nil
		}
		return errors.New("fork target already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=? AND workspace_id=?"), command.SourceThreadID, command.WorkspaceID); err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	now := time.Now().UTC()
	if command.LastTurnID != "" {
		var sourceID string
		if err := tx.GetContext(ctx, &sourceID, s.Q("SELECT codex_thread_id FROM codex_threads WHERE id=?"), command.SourceThreadID); err != nil {
			return err
		}
		if sourceID != command.CodexThread || command.CutoffSequence <= 0 {
			return errors.New("source conversation changed while forking")
		}
	}
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO codex_threads(id,workspace_id,codex_thread_id,title,status,last_sequence,created_at,updated_at) SELECT ?,workspace_id,?,?,'idle',CASE WHEN ?>0 THEN ? ELSE last_sequence END,?,? FROM codex_threads WHERE id=?`), command.TargetThreadID, codexThread, command.Title, command.CutoffSequence, command.CutoffSequence, now, now, command.SourceThreadID); err != nil {
		return err
	}
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &events, s.Q(`SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND (?=0 OR sequence<=?) ORDER BY sequence`), command.SourceThreadID, command.CutoffSequence, command.CutoffSequence); err != nil {
		return err
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)`), NewID(), command.TargetThreadID, event.Sequence, event.Kind, event.OccurredAt, event.Payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CommitGitWorktree(ctx context.Context, command protocol.GitWorktreeCreateCommand, result protocol.GitWorktreeCreateResult) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM workspaces WHERE id=? AND project_id=? AND server_id=(SELECT server_id FROM workspaces WHERE id=?)"), command.SourceWorkspaceID, command.ProjectID, command.SourceWorkspaceID); err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	if result.Path == "" || result.Branch != command.Branch || result.CommitSHA == "" {
		return errors.New("worktree result does not match command")
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,parent_workspace_id,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at)
		SELECT ?,project_id,server_id,?,?,'managed','ready','worktree',?,?,?,0,?,? FROM workspaces WHERE id=?`), command.TargetWorkspaceID, result.Path, command.Branch, command.SourceWorkspaceID, result.Branch, result.CommitSHA, time.Now().UTC(), time.Now().UTC(), command.SourceWorkspaceID)
	if err != nil {
		return err
	}
	if command.ClaudeFork != nil {
		if command.ClaudeFork.WorkspaceID != command.TargetWorkspaceID || command.ClaudeFork.SourceWorkspace != command.SourcePath || command.ClaudeFork.Workspace != result.Path {
			return errors.New("Claude worktree result mismatch")
		}
		if err = s.commitClaudeFork(ctx, tx, *command.ClaudeFork, result.ClaudeSnapshotID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if command.GeminiFork != nil {
		if command.GeminiFork.WorkspaceID != command.TargetWorkspaceID || command.GeminiFork.SourceWorkspace != command.SourcePath || command.GeminiFork.Workspace != result.Path {
			return errors.New("Gemini worktree result mismatch")
		}
		if err = s.commitGeminiFork(ctx, tx, *command.GeminiFork, result.GeminiSessionID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if command.TargetThreadID == "" {
		return tx.Commit()
	}
	if command.SourceThreadID == "" || result.CodexThread == "" {
		return errors.New("continued worktree has incomplete thread result")
	}
	var sourceCount int
	if err := tx.GetContext(ctx, &sourceCount, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=? AND workspace_id=?"), command.SourceThreadID, command.SourceWorkspaceID); err != nil {
		return err
	}
	if sourceCount != 1 {
		return sql.ErrNoRows
	}
	if command.LastTurnID != "" {
		if err := ValidateThreadForkResult(protocol.ForkThreadCommand{CodexThread: command.CodexThread, LastTurnID: command.LastTurnID}, protocol.ForkThreadResult{CodexThread: result.CodexThread, LastTurnID: result.LastTurnID}); err != nil {
			return err
		}
		var native string
		if err := tx.GetContext(ctx, &native, s.Q("SELECT codex_thread_id FROM codex_threads WHERE id=?"), command.SourceThreadID); err != nil {
			return err
		}
		if native != command.CodexThread || command.CutoffSequence <= 0 {
			return errors.New("source conversation changed while forking")
		}
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO codex_threads(id,workspace_id,codex_thread_id,title,status,last_sequence,created_at,updated_at)
		SELECT ?,?,?,?,'idle',CASE WHEN ?>0 THEN ? ELSE last_sequence END,?,? FROM codex_threads WHERE id=?`), command.TargetThreadID, command.TargetWorkspaceID, result.CodexThread, command.Title, command.CutoffSequence, command.CutoffSequence, now, now, command.SourceThreadID); err != nil {
		return err
	}
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &events, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND (?=0 OR sequence<=?) ORDER BY sequence"), command.SourceThreadID, command.CutoffSequence, command.CutoffSequence); err != nil {
		return err
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), command.TargetThreadID, event.Sequence, event.Kind, event.OccurredAt, event.Payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetThreadTitleIfDefault(ctx context.Context, id, title string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET title=?,updated_at=? WHERE id=? AND title='New session'"), title, time.Now().UTC(), id)
	return err
}

func (thread *Thread) setCapabilities() {
	thread.Capabilities = session.ResolveCapabilities(session.Codex, thread.AgentVersion, thread.CLIVersion)
}
