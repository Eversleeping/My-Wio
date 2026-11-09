package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

var ErrRequestConflict = errors.New("request_id already has different content")

// CodexRequestInput contains only client input. Native thread/turn IDs and
// workspace metadata can change before a retry and must not affect its hash.
type CodexRequestInput struct {
	Prompt          string               `json:"prompt"`
	Images          []protocol.TurnImage `json:"images"`
	Model           string               `json:"model,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	ApprovalMode    string               `json:"approval_mode,omitempty"`
}

func codexRequestHash(input CodexRequestInput) string {
	if len(input.Images) == 0 {
		input.Images = nil
	}
	raw, _ := json.Marshal(input)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (s *Store) lookupCodexRequest(ctx context.Context, db sqlx.QueryerContext, threadID, requestID, action string, input CodexRequestInput) (string, error) {
	if requestID == "" {
		return "", sql.ErrNoRows
	}
	var row struct {
		Action      string `db:"action"`
		Hash        string `db:"input_hash"`
		OperationID string `db:"operation_id"`
	}
	err := sqlx.GetContext(ctx, db, &row, s.Q("SELECT action,input_hash,operation_id FROM codex_requests WHERE thread_id=? AND request_id=?"), threadID, requestID)
	if err != nil {
		return "", err
	}
	if row.Action != action || row.Hash != codexRequestHash(input) {
		return "", ErrRequestConflict
	}
	return row.OperationID, nil
}

// LookupCodexRequest is intentionally checked before mutable session/server
// preconditions: a completed request must still replay after its turn ends.
func (s *Store) LookupCodexRequest(ctx context.Context, threadID, requestID, action string, input CodexRequestInput) (string, error) {
	return s.lookupCodexRequest(ctx, s.DB, threadID, requestID, action, input)
}

func (s *Store) saveCodexRequest(ctx context.Context, tx *sqlx.Tx, threadID, requestID, action string, input CodexRequestInput, operationID string) error {
	if requestID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.Q("INSERT INTO codex_requests(thread_id,request_id,action,input_hash,operation_id) VALUES(?,?,?,?,?)"), threadID, requestID, action, codexRequestHash(input), operationID)
	return err
}

func (s *Store) lockCodexRequestThread(ctx context.Context, tx *sqlx.Tx, threadID string) error {
	result, err := tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET status=status WHERE id=?"), threadID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func codexTurnInput(command protocol.StartTurnCommand) CodexRequestInput {
	return CodexRequestInput{Prompt: command.Prompt, Images: command.Images, Model: command.Model, ReasoningEffort: command.ReasoningEffort, ApprovalMode: command.ApprovalMode}
}

func (s *Store) QueueCodexTurnRequest(ctx context.Context, thread Thread, command protocol.StartTurnCommand, requestID string) (string, protocol.StreamEvent, error) {
	input := codexTurnInput(command)
	var operationID string
	var event protocol.StreamEvent
	err := s.inTx(ctx, s.addEventTxOptions(), func(tx *sqlx.Tx) error {
		operationID, event = "", protocol.StreamEvent{}
		if err := s.lockCodexRequestThread(ctx, tx, thread.ID); err != nil {
			return err
		}
		previous, err := s.lookupCodexRequest(ctx, tx, thread.ID, requestID, "turn", input)
		if err == nil {
			operationID = previous
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		operationID, event, err = s.queueCodexTurnTx(ctx, tx, thread, command)
		if err != nil {
			return err
		}
		return s.saveCodexRequest(ctx, tx, thread.ID, requestID, "turn", input, operationID)
	})
	return operationID, event, err
}

func (s *Store) QueueCodexSteerRequest(ctx context.Context, thread Thread, command protocol.SteerTurnCommand, requestID string) (string, protocol.StreamEvent, error) {
	input := CodexRequestInput{Prompt: command.Prompt, Images: command.Images}
	var operationID string
	var event protocol.StreamEvent
	err := s.inTx(ctx, s.addEventTxOptions(), func(tx *sqlx.Tx) error {
		operationID, event = "", protocol.StreamEvent{}
		if err := s.lockCodexRequestThread(ctx, tx, thread.ID); err != nil {
			return err
		}
		previous, err := s.lookupCodexRequest(ctx, tx, thread.ID, requestID, "steer", input)
		if err == nil {
			operationID = previous
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var active int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=? AND archived_at IS NULL AND status='running' AND codex_thread_id=?"), thread.ID, command.CodexThread); err != nil {
			return err
		}
		if active != 1 {
			return ErrThreadActive
		}
		sequence, err := s.nextEventSequence(ctx, tx, thread.ID)
		if err != nil {
			return err
		}
		turnID, err := s.latestActiveTurnID(ctx, tx, thread.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if turnID == "" || turnID != command.ExpectedTurnID {
			return ErrThreadActive
		}
		raw, err := json.Marshal(command)
		if err != nil {
			return err
		}
		operationID = NewID()
		now := time.Now().UTC()
		_, err = tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,project_id,workspace_id,thread_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,'codex.turn.steer',?,?,?)"), operationID, thread.ServerID, thread.ProjectID, thread.WorkspaceID, thread.ID, string(raw), "codex-steer:"+operationID, now)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"text": command.Prompt, "image_count": len(command.Images), "steer": true, "operation_id": operationID, "turn_id": turnID})
		event = protocol.StreamEvent{EventID: NewID(), StreamID: thread.ID, Sequence: sequence, Kind: "user.message", OccurredAt: now, Payload: payload}
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, now, string(payload)); err != nil {
			return err
		}
		return s.saveCodexRequest(ctx, tx, thread.ID, requestID, "steer", input, operationID)
	})
	return operationID, event, err
}
