package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

type questionSpec struct {
	kind, name, table, threadSelect, cliColumn string
	checkCancellation                          bool
}
type questionSession struct {
	OperationID  string     `db:"operation_id"`
	Status       string     `db:"status"`
	ArchivedAt   *time.Time `db:"archived_at"`
	ServerStatus string     `db:"server_status"`
	ServerID     string     `db:"server_id"`
	ProjectID    string     `db:"project_id"`
	WorkspaceID  string     `db:"workspace_id"`
	AgentVersion string     `db:"agent_version"`
	CLIVersion   string     `db:"cli_version"`
}

func (s *Store) QueueClaudeAnswer(ctx context.Context, threadID, requestID string, answers map[string]protocol.UserInputAnswer) (string, error) {
	return s.queueSessionAnswer(ctx, threadID, requestID, answers, questionSpec{"claude", "Claude", "claude_threads", claudeThreadSelect, "claude_version", true})
}
func (s *Store) QueueGeminiAnswer(ctx context.Context, threadID, requestID string, answers map[string]protocol.UserInputAnswer) (string, error) {
	return s.queueSessionAnswer(ctx, threadID, requestID, answers, questionSpec{"gemini", "Gemini", "gemini_threads", geminiThreadSelect, "gemini_version", false})
}

// queueSessionAnswer serializes with turn completion and history rewriting.
// Answers never take the workspace write lock held by the waiting turn.
func (s *Store) queueSessionAnswer(ctx context.Context, threadID, requestID string, answers map[string]protocol.UserInputAnswer, cfg questionSpec) (string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE "+cfg.table+" SET status=status WHERE id=?"), threadID); err != nil {
		return "", err
	}
	var thread questionSession
	if err = tx.GetContext(ctx, &thread, s.Q("SELECT operation_id,status,archived_at,server_status,server_id,project_id,workspace_id,agent_version,"+cfg.cliColumn+" AS cli_version FROM ("+cfg.threadSelect+" WHERE t.id=?) question_thread"), time.Now().UTC().Add(-ServerOnlineGracePeriod), threadID); err != nil {
		return "", err
	}
	var detail string
	if err = tx.GetContext(ctx, &detail, s.Q("SELECT payload FROM events WHERE stream_id=? AND event_id=? AND kind=?"), threadID, requestID, cfg.kind+".question"); err != nil {
		return "", err
	}
	var question struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(detail), &question) != nil {
		return "", errors.New("invalid " + cfg.name + " question")
	}
	if err = protocol.ValidateUserInputAnswers(json.RawMessage(detail), answers); err != nil {
		return "", invalidInput(err)
	}
	key := cfg.kind + "-answer:" + requestID
	var existing struct {
		ID      string `db:"id"`
		Payload string `db:"payload"`
	}
	err = tx.GetContext(ctx, &existing, s.Q("SELECT id,payload FROM agent_operations WHERE idempotency_key=?"), key)
	if err == nil {
		var command protocol.GeminiAnswerCommand
		_ = json.Unmarshal([]byte(existing.Payload), &command)
		if !reflect.DeepEqual(command.Answers, answers) {
			return "", stateConflict(cfg.name + " question already has a different answer")
		}
		return existing.ID, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if thread.OperationID != question.OperationID || thread.Status != "running" || thread.ArchivedAt != nil || thread.ServerStatus != "online" {
		return "", stateConflict(cfg.name + " question has expired or Agent is offline")
	}
	if !session.ResolveCapabilities(cfg.kind, thread.AgentVersion, thread.CLIVersion).Questions {
		return "", stateConflict(cfg.name + " questions require an Agent update")
	}
	if cfg.checkCancellation {
		var cancelled int
		if err := tx.GetContext(ctx, &cancelled, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND event_id=? AND kind=?"), threadID, requestID+":question.cancelled", cfg.kind+".question.cancelled"); err != nil {
			return "", err
		}
		if cancelled != 0 {
			return "", stateConflict(cfg.name + " question has been cancelled")
		}
	}
	// The Gemini and Claude answer envelopes have identical JSON fields.
	// Keep the persisted operation kind and idempotency prefix engine-specific.
	command := protocol.GeminiAnswerCommand{ThreadID: threadID, OperationID: question.OperationID, RequestID: requestID, Answers: answers}
	raw, _ := json.Marshal(command)
	id := NewID()
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?)`), id, thread.ServerID, thread.ProjectID, thread.WorkspaceID, cfg.kind+".question.answer", string(raw), key, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
