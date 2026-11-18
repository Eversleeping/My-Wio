package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/task"
)

const taskAttemptSelect = `SELECT id,task_id,run_id,plan_id,step_id,member_id,number,state,engine,session_id,server_id,workspace_id,operation_id,interrupt_operation_id,lease_owner,lease_generation,lease_expires_at,deadline_at,evidence_cursor,outcome_certainty,result_summary FROM task_attempts`

// RecordTaskOperationResult preserves authenticated Agent receipts separately
// from control-plane synthetic failures, including receipts arriving after a
// synthetic operation failure. It never changes the current task projection.
func (s *Store) RecordTaskOperationResult(ctx context.Context, server string, r protocol.OperationResult, unknown bool) error {
	var a task.Attempt
	err := s.DB.GetContext(ctx, &a, s.Q(taskAttemptSelect+" WHERE operation_id=? AND server_id=?"), r.OperationID, server)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	class := "agent_result"
	if unknown {
		class = "ownership_unknown"
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, s.Q("INSERT INTO task_attempt_evidence(attempt_id,source_event_id,classification,payload_json,content_hash,acquired_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING"), a.ID, "operation:"+r.OperationID+":"+class+":"+HashToken(string(raw)), class, string(raw), HashToken(string(raw)), time.Now().UTC())
	return err
}

func taskDBTimeTx(ctx context.Context, tx *sqlx.Tx) (time.Time, error) {
	var raw any
	query := "SELECT CURRENT_TIMESTAMP"
	// PostgreSQL CURRENT_TIMESTAMP is the transaction start time, which can
	// predate a long lock wait. Lease fences require the current database clock.
	if tx.DriverName() == "pgx" {
		query = "SELECT clock_timestamp()"
	}
	if err := tx.GetContext(ctx, &raw, query); err != nil {
		return time.Time{}, err
	}
	if at, ok := raw.(time.Time); ok {
		return at.UTC(), nil
	}
	value := fmt.Sprint(raw)
	if b, ok := raw.([]byte); ok {
		value = string(b)
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid database timestamp")
}

func (s *Store) ClaimTaskAttempt(ctx context.Context, id, owner string) (task.Fence, bool, error) {
	var fence task.Fence
	claimed := false
	if owner == "" {
		return fence, false, task.Fail(task.Invalid, "worker identity is required")
	}
	err := s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		claimed = false
		fence = task.Fence{}
		var a task.Attempt
		if err := tx.GetContext(ctx, &a, s.Q(taskAttemptSelect+" WHERE id=?"), id); err != nil {
			return err
		}
		if err := s.lockTaskTx(ctx, tx, a.TaskID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &a, s.Q(taskAttemptSelect+" WHERE id=?"), id); err != nil {
			return err
		}
		if a.State != "queued" && a.State != "running" && a.State != "unknown" {
			return nil
		}
		now, err := taskDBTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		if a.LeaseOwner != nil && *a.LeaseOwner != owner && a.LeaseExpiresAt != nil && a.LeaseExpiresAt.After(now) {
			return nil
		}
		generation := a.LeaseGeneration
		if a.LeaseOwner == nil || *a.LeaseOwner != owner {
			generation++
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_attempts SET lease_owner=?,lease_generation=?,lease_expires_at=? WHERE id=?"), owner, generation, now.Add(30*time.Second), id); err != nil {
			return err
		}
		fence = task.Fence{AttemptID: id, Owner: owner, Generation: generation}
		claimed = true
		return nil
	})
	return fence, claimed, err
}

func (s *Store) taskObservationTx(ctx context.Context, tx *sqlx.Tx, a task.Attempt) (string, string, int64, error) {
	var op Operation
	if a.OperationID == nil {
		return "unknown", "missing operation association", a.EvidenceCursor, nil
	}
	if err := tx.GetContext(ctx, &op, s.Q(operationSelect+" WHERE id=?"), *a.OperationID); err != nil {
		return "unknown", "", a.EvidenceCursor, err
	}
	var receipts []struct {
		Class   string `db:"classification"`
		Payload string `db:"payload_json"`
	}
	if err := tx.SelectContext(ctx, &receipts, s.Q("SELECT classification,payload_json FROM task_attempt_evidence WHERE attempt_id=? AND classification IN ('agent_result','ownership_unknown') ORDER BY acquired_at,source_event_id"), a.ID); err != nil {
		return "unknown", "", a.EvidenceCursor, err
	}
	actual := ""
	summary := ""
	uncertain := false
	for _, row := range receipts {
		if row.Class == "ownership_unknown" {
			uncertain = true
			continue
		}
		var r protocol.OperationResult
		if json.Unmarshal([]byte(row.Payload), &r) != nil {
			continue
		}
		state := r.Status
		if state == "interrupted" {
			state = "cancelled"
		}
		if state != "succeeded" && state != "failed" && state != "cancelled" {
			continue
		}
		if actual != "" && actual != state {
			return "unknown", "conflicting Agent receipts", a.EvidenceCursor, nil
		}
		actual = state
		summary = row.Payload
	}
	if a.Engine != "codex" {
		if actual != "" {
			return actual, summary, a.EvidenceCursor, nil
		}
		if uncertain || op.Status == "failed" || op.Status == "cancelled" {
			return "unknown", "operation ended without trusted termination evidence", a.EvidenceCursor, nil
		}
		if op.Status == "running" || op.Status == "delivered" {
			return "running", "", a.EvidenceCursor, nil
		}
		return "queued", "", a.EvidenceCursor, nil
	}
	start, end, err := s.collaborationTurnBounds(ctx, tx, a.SessionID, op.ID)
	if err != nil {
		return "unknown", "", a.EvidenceCursor, err
	}
	if start == 0 {
		return "unknown", "missing original Codex input boundary", a.EvidenceCursor, nil
	}
	events, err := s.eventRowsQuery(ctx, tx, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence>? AND (?=0 OR sequence<?) AND kind IN ('turn.accepted','codex.turn.completed','codex.turn.failed','codex.turn.cancelled') ORDER BY sequence"), a.SessionID, start, end, end)
	if err != nil {
		return "unknown", "", a.EvidenceCursor, err
	}
	turn := ""
	cursor := a.EvidenceCursor
	for _, e := range events {
		var p struct {
			TurnID string `json:"turn_id"`
			Turn   struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		if e.Kind == protocol.EventTurnAccepted {
			if p.TurnID != "" {
				turn = p.TurnID
			}
			continue
		}
		terminal := p.Turn.ID
		if terminal == "" {
			terminal = p.TurnID
		}
		if turn == "" || terminal != turn {
			continue
		}
		state := ""
		switch p.Turn.Status {
		case "completed":
			state = "succeeded"
		case "failed":
			state = "failed"
		case "interrupted", "cancelled":
			state = "cancelled"
		}
		if e.Kind == protocol.EventCodexTurnFailed {
			state = "failed"
		}
		if e.Kind == protocol.EventCodexTurnCancelled {
			state = "cancelled"
		}
		if state == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO task_attempt_evidence(attempt_id,source_event_id,classification,payload_json,content_hash,acquired_at) VALUES(?,?,'codex_terminal',?,?,?) ON CONFLICT DO NOTHING"), a.ID, e.EventID, string(e.Payload), HashToken(string(e.Payload)), time.Now().UTC()); err != nil {
			return "unknown", "", cursor, err
		}
		cursor = e.Sequence
		return state, string(e.Payload), cursor, nil
	}
	if uncertain || op.Status == "failed" || op.Status == "cancelled" {
		return "unknown", "Codex termination is unconfirmed", cursor, nil
	}
	if turn != "" {
		return "running", "", cursor, nil
	}
	return "queued", "", cursor, nil
}

func (s *Store) ReconcileTaskAttempt(ctx context.Context, f task.Fence) error {
	var hint task.Attempt
	if err := s.DB.GetContext(ctx, &hint, s.Q(taskAttemptSelect+" WHERE id=?"), f.AttemptID); err != nil {
		return err
	}
	return s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), hint.ServerID); err != nil {
			return err
		}
		if err := s.lockTaskTx(ctx, tx, hint.TaskID); err != nil {
			return err
		}
		var a task.Attempt
		if err := tx.GetContext(ctx, &a, s.Q(taskAttemptSelect+" WHERE id=?"), f.AttemptID); err != nil {
			return err
		}
		now, err := taskDBTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		if a.LeaseOwner == nil || *a.LeaseOwner != f.Owner || a.LeaseGeneration != f.Generation || a.LeaseExpiresAt == nil || !a.LeaseExpiresAt.After(now) {
			return task.Fail(task.Conflict, "attempt lease changed")
		}
		if a.State != "queued" && a.State != "running" && a.State != "unknown" {
			return nil
		}
		r, err := s.taskRunTx(ctx, tx, a.TaskID, a.RunID)
		if err != nil {
			return err
		}
		state, summary, cursor, err := s.taskObservationTx(ctx, tx, a)
		if err != nil {
			return err
		}
		terminal := state == "succeeded" || state == "failed" || state == "cancelled"
		if terminal && state == "succeeded" {
			summary, err = s.taskResultSummaryTx(ctx, tx, a, summary)
			if err != nil {
				return err
			}
		}
		if terminal {
			if err := tx.GetContext(ctx, &cursor, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), a.SessionID); err != nil {
				return err
			}
		}
		if !terminal && a.DeadlineAt != nil && !a.DeadlineAt.After(now) {
			state = "unknown"
			summary = "step deadline reached; awaiting confirmed termination"
		}
		if !terminal && (r.State == "cancelling" || state == "unknown" && a.DeadlineAt != nil && !a.DeadlineAt.After(now)) {
			if err := s.taskInterruptTx(ctx, tx, a); err != nil {
				return err
			}
		}
		certainty := "unknown"
		var ended *time.Time
		if terminal {
			certainty = "confirmed"
			ended = &now
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_attempts SET state=?,outcome_certainty=?,result_summary=?,evidence_cursor=?,ended_at=?,lease_expires_at=? WHERE id=? AND lease_generation=? AND lease_owner=?"), state, certainty, summary, cursor, ended, now.Add(30*time.Second), a.ID, f.Generation, f.Owner); err != nil {
			return err
		}
		stepState, code := "active", any(nil)
		if terminal {
			if state == "succeeded" {
				stepState = "succeeded"
				definition, err := s.taskStepDefinitionTx(ctx, tx, a)
				if err != nil {
					return err
				}
				switch definition.CompletionPolicy {
				case "artifact":
					stepState, code = "blocked", "artifact_required"
				case "review":
					stepState, code = "blocked", "review_required"
				}
			} else {
				stepState = "blocked"
				code = "attempt_" + state
			}
			if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_session_bindings SET active_attempt_id=NULL WHERE active_attempt_id=?"), a.ID); err != nil {
				return err
			}
		} else if state == "unknown" {
			stepState = "blocked"
			code = "execution_unknown"
		}
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE task_step_runs SET state=?,block_code=?,version=version+1 WHERE run_id=? AND step_id=? AND current_attempt_id=?"), stepState, code, a.RunID, a.StepID, a.ID); err != nil {
			return err
		}
		if state != a.State {
			return s.taskEventTx(ctx, tx, a.TaskID, "task.attempt.state.changed", task.Meta{Actor: "task:" + a.TaskID}, map[string]any{"run_id": a.RunID, "step_id": a.StepID, "attempt_id": a.ID, "state": state, "certainty": certainty})
		}
		return nil
	})
}

func (s *Store) taskResultSummaryTx(ctx context.Context, tx *sqlx.Tx, a task.Attempt, termination string) (string, error) {
	start, end, err := s.collaborationTurnBounds(ctx, tx, a.SessionID, *a.OperationID)
	if err != nil {
		return "", err
	}
	rows, err := s.eventRowsQuery(ctx, tx, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence>? AND (?=0 OR sequence<?) AND kind IN ('codex.item.completed','claude.message','gemini.message') ORDER BY sequence DESC LIMIT 100"), a.SessionID, start, end, end)
	if err != nil {
		return "", err
	}
	messages := []protocol.StreamEvent{}
	size := 0
	truncated := false
	for i := len(rows) - 1; i >= 0; i-- {
		if size+len(rows[i].Payload) > 64<<10 {
			truncated = true
			continue
		}
		size += len(rows[i].Payload)
		messages = append(messages, rows[i])
	}
	if len(rows) == 100 {
		truncated = true
	}
	raw, err := json.Marshal(struct {
		Termination json.RawMessage        `json:"termination"`
		Messages    []protocol.StreamEvent `json:"messages"`
		Truncated   bool                   `json:"truncated"`
	}{json.RawMessage(termination), messages, truncated})
	return string(raw), err
}

func (s *Store) taskInterruptTx(ctx context.Context, tx *sqlx.Tx, a task.Attempt) error {
	if a.InterruptOperationID != nil || a.OperationID == nil {
		return nil
	}
	var payload any
	kind := ""
	switch a.Engine {
	case "claude":
		kind = protocol.OpClaudeTurnInterrupt
		payload = protocol.ClaudeInterruptCommand{ThreadID: a.SessionID, OperationID: *a.OperationID}
	case "gemini":
		kind = protocol.OpGeminiTurnInterrupt
		payload = protocol.GeminiInterruptCommand{ThreadID: a.SessionID, OperationID: *a.OperationID}
	case "codex":
		// Wait for the accepted turn identity before interrupting Codex. A
		// successful start is still observed; it is never cancelled locally.
		var native string
		if err := tx.GetContext(ctx, &native, s.Q("SELECT codex_thread_id FROM codex_threads WHERE id=?"), a.SessionID); err != nil {
			return err
		}
		start, end, err := s.collaborationTurnBounds(ctx, tx, a.SessionID, *a.OperationID)
		if err != nil {
			return err
		}
		var accepted []struct {
			Payload string `db:"payload"`
		}
		if err := tx.SelectContext(ctx, &accepted, s.Q("SELECT payload FROM events WHERE stream_id=? AND sequence>? AND (?=0 OR sequence<?) AND kind='turn.accepted' ORDER BY sequence"), a.SessionID, start, end, end); err != nil {
			return err
		}
		turn := ""
		for _, e := range accepted {
			var p struct {
				TurnID string `json:"turn_id"`
			}
			_ = json.Unmarshal([]byte(e.Payload), &p)
			if p.TurnID != "" {
				turn = p.TurnID
			}
		}
		if turn == "" || native == "" {
			return nil
		}
		kind = protocol.OpCodexTurnInterrupt
		payload = protocol.InterruptTurnCommand{ThreadID: a.SessionID, CodexThread: native, TurnID: turn}
	}
	key := "task-interrupt:" + a.ID
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	op := NewID()
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(idempotency_key) DO NOTHING"), op, a.ServerID, a.WorkspaceID, kind, string(raw), key, time.Now().UTC()); err != nil {
		return err
	}
	if err := tx.GetContext(ctx, &op, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), key); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.Q("UPDATE task_attempts SET interrupt_operation_id=? WHERE id=?"), op, a.ID)
	return err
}
