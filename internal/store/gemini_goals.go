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
)

var ErrGeminiGoalChanged = errors.New("Gemini goal changed or cannot be resumed")

type GeminiGoal struct {
	ID               string    `db:"id" json:"id"`
	ThreadID         string    `db:"thread_id" json:"thread_id"`
	Objective        string    `db:"objective" json:"objective"`
	ImagesJSON       string    `db:"images_json" json:"-"`
	Status           string    `db:"status" json:"status"`
	OperationID      string    `db:"operation_id" json:"operation_id"`
	Continuations    int       `db:"continuations" json:"continuations"`
	MaxContinuations int       `db:"max_continuations" json:"max_continuations"`
	MaxSeconds       int       `db:"max_seconds" json:"max_seconds"`
	Report           string    `db:"report" json:"-"`
	Evidence         string    `db:"evidence" json:"evidence"`
	Message          string    `db:"message" json:"message"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
	UpdatedAt        time.Time `db:"updated_at" json:"updated_at"`
}

func ValidGeminiGoalLimits(continuations, seconds int) bool {
	return continuations >= 1 && continuations <= 100 && seconds >= 60 && seconds <= 86400
}

func (s *Store) GeminiGoal(ctx context.Context, threadID string) (*GeminiGoal, error) {
	var goal GeminiGoal
	err := s.DB.GetContext(ctx, &goal, s.Q("SELECT * FROM gemini_goals WHERE thread_id=? ORDER BY created_at DESC,id DESC LIMIT 1"), threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &goal, err
}

// The goal and its first/next operation are claimed in the same transaction as
// the thread and workspace. A restarted control plane must not replay it.
func (s *Store) QueueGeminiGoal(ctx context.Context, thread GeminiThread, prompt, model, mode, thinking string, images []protocol.TurnImage, goal *protocol.GeminiGoalCommand) (protocol.StreamEvent, error) {
	if goal == nil || !ValidGeminiGoalLimits(goal.MaxContinuations, goal.MaxSeconds) {
		return protocol.StreamEvent{}, errors.New("invalid Gemini goal limits")
	}
	return s.queueGeminiTurn(ctx, thread, prompt, model, mode, "", images, goal, thinking)
}

func (s *Store) claimGeminiGoal(ctx context.Context, tx *sqlx.Tx, threadID, opID string, goal *protocol.GeminiGoalCommand, images *[]protocol.TurnImage, now time.Time) error {
	if goal.ID == "" {
		if strings.TrimSpace(goal.Objective) == "" || len(goal.Objective) > 256*1024 {
			return errors.New("invalid Gemini goal objective")
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE gemini_goals SET status='cancelled',updated_at=? WHERE thread_id=? AND status IN ('paused','blocked')"), now, threadID); err != nil {
			return err
		}
		goal.ID = NewID()
		imagesJSON, err := json.Marshal(*images)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO gemini_goals(id,thread_id,objective,images_json,status,operation_id,continuations,max_continuations,max_seconds,created_at,updated_at)
 VALUES(?,?,?,?,'active',?,0,?,?,?,?)`), goal.ID, threadID, goal.Objective, string(imagesJSON), opID, goal.MaxContinuations, goal.MaxSeconds, now, now)
		return err
	}
	var existing GeminiGoal
	if err := tx.GetContext(ctx, &existing, s.Q("SELECT * FROM gemini_goals WHERE id=? AND thread_id=?"), goal.ID, threadID); err != nil {
		return ErrGeminiGoalChanged
	}
	if existing.Status != "paused" && existing.Status != "blocked" {
		return ErrGeminiGoalChanged
	}
	// A goal can be paused before its first prompt reaches the CLI. Preserve
	// its original images even when there is no native history to resume.
	if err := json.Unmarshal([]byte(existing.ImagesJSON), images); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.Q(`UPDATE gemini_goals SET status='active',operation_id=?,max_continuations=?,max_seconds=?,report='',evidence='',message='',updated_at=?
 WHERE id=? AND thread_id=? AND status IN ('paused','blocked')`), opID, goal.MaxContinuations, goal.MaxSeconds, now, goal.ID, threadID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrGeminiGoalChanged
	}
	goal.Objective, goal.Continuations = existing.Objective, existing.Continuations
	return nil
}

// Only an event from the operation currently owning this goal can advance it.
// A pending stop may fail, so retain its buffered reports while the operation
// can still be running. Status is finalized only after a normal operation end.
func (s *Store) UpdateGeminiGoalProgress(ctx context.Context, threadID string, p protocol.GeminiGoalProgress) error {
	if p.GoalID == "" || p.OperationID == "" || p.Continuations < 0 || (p.Report != "" && p.Report != "completed" && p.Report != "blocked") || len(p.Evidence) > 16000 || (p.Report != "" && strings.TrimSpace(p.Evidence) == "") {
		return errors.New("invalid Gemini goal progress")
	}
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE gemini_goals SET continuations=?,report=CASE WHEN ?<>'' THEN ? ELSE report END,
 evidence=CASE WHEN ?<>'' THEN ? ELSE evidence END,updated_at=?
 WHERE id=? AND thread_id=? AND operation_id=? AND continuations<=? AND
 (status='active' OR (status IN ('paused','cancelled') AND EXISTS
   (SELECT 1 FROM gemini_threads t JOIN agent_operations o ON o.id=t.operation_id
    WHERE t.id=gemini_goals.thread_id AND t.operation_id=gemini_goals.operation_id
      AND t.status IN ('interrupting','interrupted') AND o.status IN ('delivered','running'))))`), p.Continuations, p.Report, p.Report, p.Report, p.Evidence, time.Now().UTC(), p.GoalID, threadID, p.OperationID, p.Continuations)
	return err
}

func (s *Store) CancelInactiveGeminiGoal(ctx context.Context, threadID, goalID string) error {
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE gemini_goals SET status='cancelled',updated_at=? WHERE id=? AND thread_id=? AND status IN ('paused','blocked')"), time.Now().UTC(), goalID, threadID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrGeminiGoalChanged
	}
	return nil
}
