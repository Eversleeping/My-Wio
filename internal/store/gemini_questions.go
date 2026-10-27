package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// QueueGeminiAnswer serializes with turn completion and history rewriting.
// Answers never take the workspace write lock held by the waiting turn.
func (s *Store) QueueGeminiAnswer(ctx context.Context, threadID, requestID string, answers map[string]protocol.UserInputAnswer) (string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE gemini_threads SET status=status WHERE id=?"), threadID); err != nil {
		return "", err
	}
	var thread GeminiThread
	if err = tx.GetContext(ctx, &thread, s.Q(geminiThreadSelect+" WHERE t.id=?"), time.Now().UTC().Add(-ServerOnlineGracePeriod), threadID); err != nil {
		return "", err
	}
	var detail string
	if err = tx.GetContext(ctx, &detail, s.Q("SELECT payload FROM events WHERE stream_id=? AND event_id=? AND kind='gemini.question'"), threadID, requestID); err != nil {
		return "", errors.New("Gemini question not found")
	}
	var question struct {
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal([]byte(detail), &question) != nil {
		return "", errors.New("invalid Gemini question")
	}
	if err = protocol.ValidateUserInputAnswers(json.RawMessage(detail), answers); err != nil {
		return "", err
	}
	key := "gemini-answer:" + requestID
	var existing struct {
		ID      string `db:"id"`
		Payload string `db:"payload"`
	}
	err = tx.GetContext(ctx, &existing, s.Q("SELECT id,payload FROM agent_operations WHERE idempotency_key=?"), key)
	if err == nil {
		var command protocol.GeminiAnswerCommand
		_ = json.Unmarshal([]byte(existing.Payload), &command)
		if !reflect.DeepEqual(command.Answers, answers) {
			return "", errors.New("Gemini question already has a different answer")
		}
		return existing.ID, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if thread.OperationID != question.OperationID || thread.Status != "running" || thread.ArchivedAt != nil || thread.ServerStatus != "online" {
		return "", errors.New("Gemini question has expired or Agent is offline")
	}
	command := protocol.GeminiAnswerCommand{ThreadID: threadID, OperationID: question.OperationID, RequestID: requestID, Answers: answers}
	raw, _ := json.Marshal(command)
	id := NewID()
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,'gemini.question.answer',?,?,?)`), id, thread.ServerID, thread.ProjectID, thread.WorkspaceID, string(raw), key, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
