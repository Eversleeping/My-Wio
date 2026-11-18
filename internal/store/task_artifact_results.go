package store

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/task"
	"github.com/wio-platform/wio/internal/taskartifact"
)

func (s *Store) CompleteTaskArtifact(ctx context.Context, op Operation, receipt protocol.OperationResult, unknown bool) (string, error) {
	state, message := "failed", receipt.Message
	if unknown {
		state = "unknown"
	}
	if len(message) > 1000 {
		message = "Agent could not complete the artifact operation"
	}
	now := time.Now().UTC()
	var id string
	err := s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		var row struct {
			ID      string `db:"id"`
			Task    string `db:"task_id"`
			State   string `db:"state"`
			Payload string `db:"expected"`
		}
		capture := op.Kind == protocol.OpTaskArtifactCapture
		query := "SELECT id,task_id,state,paths_json AS expected FROM task_artifacts WHERE operation_id=? AND workspace_id=?"
		if !capture {
			query = `SELECT h.id,h.task_id,h.state,a.manifest_hash AS expected FROM task_artifact_handoffs h
				JOIN task_artifacts a ON a.task_id=h.task_id AND a.id=h.artifact_id WHERE h.operation_id=? AND h.workspace_id=?`
		}
		if err := tx.GetContext(ctx, &row, s.Q(query), op.ID, op.WorkspaceID); err != nil {
			return err
		}
		if err := s.lockTaskTx(ctx, tx, row.Task); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &row, s.Q(query), op.ID, op.WorkspaceID); err != nil || row.State != "queued" {
			return err
		}
		var manifest protocol.ArtifactManifest
		var digest string
		var raw *string
		if !unknown && receipt.Status == "succeeded" {
			valid := false
			if capture {
				var expected []string
				if json.Unmarshal(receipt.Data, &manifest) == nil && json.Unmarshal([]byte(row.Payload), &expected) == nil {
					digest, _ = taskartifact.Validate(manifest)
					paths := make([]string, 0, len(manifest.Files))
					for _, file := range manifest.Files {
						paths = append(paths, file.Path)
					}
					valid = digest != "" && slices.Equal(paths, expected)
					if valid {
						encoded, _ := json.Marshal(manifest)
						content := string(encoded)
						raw = &content
					}
				}
			} else {
				var r protocol.TaskArtifactReceipt
				var command struct{ Action string }
				valid = json.Unmarshal(receipt.Data, &r) == nil && json.Unmarshal([]byte(op.Payload), &command) == nil &&
					r.Digest == row.Payload && r.Action == command.Action
			}
			if valid {
				state, message = "ready", ""
			} else {
				state, message, digest = "failed", "Agent artifact evidence did not match the request", ""
			}
		}
		status := "failed"
		if state == "ready" {
			status = "succeeded"
		}
		if err := s.completeActiveOperation(ctx, tx, op.ID, status, message, "{}", now); err != nil {
			return err
		}
		var err error
		kind := "task.artifact.handoff.changed"
		if capture {
			kind = "task.artifact.updated"
			if state != "ready" {
				manifest.BaseSHA = ""
			}
			_, err = tx.ExecContext(ctx, s.Q("UPDATE task_artifacts SET state=?,manifest_json=?,manifest_hash=?,base_sha=?,error=? WHERE id=?"), state, raw, digest, manifest.BaseSHA, message, row.ID)
		} else {
			_, err = tx.ExecContext(ctx, s.Q("UPDATE task_artifact_handoffs SET state=?,error=? WHERE id=?"), state, message, row.ID)
		}
		if err != nil {
			return err
		}
		if capture && state == "ready" {
			if _, err := tx.ExecContext(ctx, s.Q(`UPDATE task_step_runs SET artifact_id=?,state='succeeded',block_code=NULL,version=version+1
                WHERE task_id=? AND block_code='artifact_required' AND current_attempt_id=(SELECT attempt_id FROM task_artifacts WHERE id=?)`), row.ID, row.Task, row.ID); err != nil {
				return err
			}
		}
		id = row.Task
		return s.taskArtifactEventTx(ctx, tx, row.Task, task.Meta{Actor: "agent:" + op.ServerID}, kind, map[string]any{"id": row.ID, "state": state})
	})
	return id, err
}
