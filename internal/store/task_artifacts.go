package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/task"
	"github.com/wio-platform/wio/internal/taskartifact"
)

const artifactSelect = "SELECT id,task_id,run_id,attempt_id,workspace_id,revision,operation_id,state,manifest_hash,base_sha,error,created_at FROM task_artifacts"

func (s *Store) artifactWorkspaceTx(ctx context.Context, tx *sqlx.Tx, project, id string) (Workspace, error) {
	var workspace Workspace
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=status WHERE id=? AND project_id=?"), id, project); err != nil {
		return workspace, err
	}
	if err := tx.GetContext(ctx, &workspace, s.Q("SELECT * FROM workspaces WHERE id=? AND project_id=?"), id, project); err != nil {
		return workspace, err
	}
	if workspace.Status != "ready" {
		return workspace, task.Fail(task.StateConflict, "workspace is not ready")
	}
	var server struct {
		Version string                   `db:"agent_version"`
		Seen    *time.Time               `db:"last_seen_at"`
		Support capabilities.Declaration `db:"declared_capabilities"`
	}
	if err := tx.GetContext(ctx, &server, s.Q("SELECT s.agent_version,s.last_seen_at,"+serverCapabilitySelect+" FROM servers s WHERE s.id=? AND s.revoked_at IS NULL"), workspace.ServerID); err != nil {
		return workspace, err
	}
	if server.Seen == nil || time.Since(*server.Seen) > ServerOnlineGracePeriod {
		return workspace, task.Fail(task.StateConflict, "workspace Agent is offline")
	}
	if !capabilities.Supports(protocol.CapabilityTaskArtifacts, server.Version, server.Support) {
		return workspace, task.Fail(task.Unsupported, "update the workspace Agent to support task artifacts")
	}
	var active int
	if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM task_attempts WHERE workspace_id=? AND state IN ('queued','running','unknown')"), id); err != nil {
		return workspace, err
	}
	busy, err := s.workspaceWriteBusy(ctx, tx, id, false)
	if err != nil {
		return workspace, err
	}
	if active > 0 || busy {
		return workspace, task.Fail(task.StateConflict, "workspace has active execution or writes")
	}
	return workspace, nil
}

func (s *Store) queueArtifactOperationTx(ctx context.Context, tx *sqlx.Tx, w Workspace, id, kind string, command any) (string, error) {
	raw, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	op := NewID()
	// Both reads and writes reserve the workspace while capturing or reproducing
	// a snapshot so a new native turn cannot invalidate its input.
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at)
		VALUES(?,?,?,?,?,?,1,?,?)`), op, w.ServerID, w.ProjectID, w.ID, kind, string(raw), "task-artifact:"+id, time.Now().UTC())
	return op, err
}

func (s *Store) CaptureTaskArtifact(ctx context.Context, project, id string, in task.CaptureArtifact) (task.Artifact, error) {
	var out task.Artifact
	err := s.taskCommand(ctx, project, id, "capture_artifact", in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if t.Version != in.ExpectedVersion {
			return task.Fail(task.Conflict, "task version changed")
		}
		if t.ArchivedAt != nil || t.CurrentRunID == nil {
			return task.Fail(task.StateConflict, "task has no current run accepting artifacts")
		}
		if err := taskartifact.Paths(in.Paths); err != nil {
			return task.Fail(task.Invalid, err.Error())
		}
		var a task.Attempt
		if err := tx.GetContext(ctx, &a, s.Q(taskAttemptSelect+" WHERE task_id=? AND run_id=? AND id=?"), id, *t.CurrentRunID, in.AttemptID); err != nil {
			return err
		}
		if a.State != "succeeded" || a.OutcomeCertainty != "confirmed" {
			return task.Fail(task.StateConflict, "capture needs a successfully completed attempt")
		}
		var current string
		if err := tx.GetContext(ctx, &current, s.Q("SELECT COALESCE(current_attempt_id,'') FROM task_step_runs WHERE run_id=? AND step_id=?"), a.RunID, a.StepID); err != nil {
			return err
		}
		if current != a.ID {
			return task.Fail(task.StateConflict, "attempt has been replaced")
		}
		workspace, err := s.artifactWorkspaceTx(ctx, tx, project, a.WorkspaceID)
		if err != nil {
			return err
		}
		if workspace.ServerID != a.ServerID {
			return task.Fail(task.StateConflict, "attempt source workspace has moved")
		}
		var revision int64
		if err := tx.GetContext(ctx, &revision, s.Q("SELECT COALESCE(MAX(revision),0)+1 FROM task_artifacts WHERE task_id=?"), id); err != nil {
			return err
		}
		if revision > 100 {
			return task.Fail(task.StateConflict, "task artifact limit reached")
		}
		now, artifact := time.Now().UTC(), NewID()
		op, err := s.queueArtifactOperationTx(ctx, tx, workspace, artifact, protocol.OpTaskArtifactCapture, protocol.TaskArtifactCapture{Workspace: workspace.Path, Paths: in.Paths})
		if err != nil {
			return err
		}
		paths, _ := json.Marshal(in.Paths)
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO task_artifacts(id,task_id,run_id,attempt_id,workspace_id,revision,operation_id,state,paths_json,created_at)
			VALUES(?,?,?,?,?,?,?,'queued',?,?)`), artifact, id, a.RunID, a.ID, workspace.ID, revision, op, string(paths), now); err != nil {
			return err
		}
		out = task.Artifact{ID: artifact, TaskID: id, RunID: a.RunID, AttemptID: a.ID, WorkspaceID: workspace.ID, Revision: revision, OperationID: op, State: "queued", CreatedAt: now}
		return s.taskArtifactEventTx(ctx, tx, id, in.Meta, "task.artifact.created", map[string]any{"artifact_id": artifact, "attempt_id": a.ID, "state": "queued"})
	})
	return out, err
}

func (s *Store) HandoffTaskArtifact(ctx context.Context, project, id, artifact string, in task.HandoffArtifact) (task.ArtifactHandoff, error) {
	var out task.ArtifactHandoff
	err := s.taskCommand(ctx, project, id, "handoff_artifact:"+artifact, in.Meta, in, &out, func(tx *sqlx.Tx) error {
		t, err := s.taskTx(ctx, tx, project, id)
		if err != nil {
			return err
		}
		if in.ExpectedVersion != t.Version {
			return task.Fail(task.Conflict, "task version changed")
		}
		if t.ArchivedAt != nil {
			return task.Fail(task.StateConflict, "restore the task before preparing a workspace")
		}
		if in.Action != "apply" && in.Action != "preflight" {
			return task.Fail(task.Invalid, "choose apply or preflight")
		}
		var a task.Artifact
		if err := tx.GetContext(ctx, &a, s.Q(artifactSelect+" WHERE task_id=? AND id=?"), id, artifact); err != nil {
			return err
		}
		if a.State != "ready" || in.Digest != a.Digest {
			return task.Fail(task.Conflict, "artifact is not ready or its digest does not match")
		}
		w, err := s.artifactWorkspaceTx(ctx, tx, project, in.WorkspaceID)
		if err != nil {
			return err
		}
		if in.Action == "apply" {
			var source Workspace
			if err := tx.GetContext(ctx, &source, s.Q("SELECT * FROM workspaces WHERE id=? AND project_id=?"), a.WorkspaceID, project); err != nil {
				return err
			}
			if w.ID == source.ID || w.Kind != "worktree" || w.ManagementMode != "managed" ||
				(w.ServerID == source.ServerID && (containsArtifactPath(w.Path, source.Path) || containsArtifactPath(source.Path, w.Path))) {
				return task.Fail(task.Invalid, "prepare a separate managed worktree for the artifact")
			}
			var unknown int
			if err := tx.GetContext(ctx, &unknown, s.Q(`SELECT COUNT(*) FROM task_artifact_handoffs WHERE workspace_id=? AND action='apply' AND state='unknown'`), w.ID); err != nil {
				return err
			}
			if unknown > 0 {
				return task.Fail(task.StateConflict, "prior handoff to this workspace is unconfirmed; use another worktree")
			}
		}
		var raw string
		var manifest protocol.ArtifactManifest
		if err := tx.GetContext(ctx, &raw, s.Q("SELECT manifest_json FROM task_artifacts WHERE id=?"), artifact); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
			return err
		}
		digest, err := taskartifact.Validate(manifest)
		if err != nil || digest != a.Digest {
			return task.Fail(task.Conflict, "stored artifact evidence is invalid")
		}
		var previous *protocol.ArtifactManifest
		if in.Action == "apply" {
			var old string
			err := tx.GetContext(ctx, &old, s.Q(`SELECT a.manifest_json FROM task_artifact_handoffs h
   JOIN task_artifacts a ON a.task_id=h.task_id AND a.id=h.artifact_id
   JOIN tasks previous_task ON previous_task.id=h.task_id
   WHERE h.workspace_id=? AND previous_task.project_id=?
   AND h.action='apply' AND h.state='ready' ORDER BY h.created_at DESC,h.id DESC LIMIT 1`), w.ID, project)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				previous = new(protocol.ArtifactManifest)
				if err := json.Unmarshal([]byte(old), previous); err != nil {
					return err
				}
			}
		}
		now, handoff := time.Now().UTC(), NewID()
		op, err := s.queueArtifactOperationTx(ctx, tx, w, handoff, protocol.OpTaskArtifactHandoff,
			protocol.TaskArtifactHandoff{Workspace: w.Path, Action: in.Action, Digest: digest, Manifest: manifest, Previous: previous})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO task_artifact_handoffs(id,task_id,artifact_id,workspace_id,action,operation_id,state,created_at)
			VALUES(?,?,?,?,?,?,'queued',?)`), handoff, id, artifact, w.ID, in.Action, op, now); err != nil {
			return err
		}
		out = task.ArtifactHandoff{ID: handoff, TaskID: id, ArtifactID: artifact, WorkspaceID: w.ID, Action: in.Action, OperationID: op, State: "queued", CreatedAt: now}
		return s.taskArtifactEventTx(ctx, tx, id, in.Meta, "task.artifact.handoff.changed", map[string]any{"artifact_id": artifact, "handoff_id": handoff, "state": "queued"})
	})
	return out, err
}

func containsArtifactPath(root, other string) bool {
	// Artifacts execute on Linux Agents. Interpret their workspace names as
	// POSIX paths even when the control plane was built on Windows.
	if !path.IsAbs(root) || !path.IsAbs(other) {
		return true
	}
	root, other = path.Clean(root), path.Clean(other)
	return root == other || strings.HasPrefix(other, strings.TrimSuffix(root, "/")+"/")
}

func (s *Store) taskArtifactEventTx(ctx context.Context, tx *sqlx.Tx, id string, meta task.Meta, kind string, payload any) error {
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE tasks SET version=version+1,updated_at=? WHERE id=?"), time.Now().UTC(), id); err != nil {
		return err
	}
	return s.taskEventTx(ctx, tx, id, kind, meta, payload)
}

func (s *Store) TaskArtifacts(ctx context.Context, project, id string) (task.Artifacts, error) {
	out := task.Artifacts{Items: []task.Artifact{}, Handoffs: []task.ArtifactHandoff{}}
	err := s.taskReadTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := s.taskTx(ctx, tx, project, id); err != nil {
			return err
		}
		out.Items = []task.Artifact{}
		if err := tx.SelectContext(ctx, &out.Items, s.Q(artifactSelect+" WHERE task_id=? ORDER BY revision DESC"), id); err != nil {
			return err
		}
		query := `SELECT a.id,json_extract(f.value,'$.path') AS path,
			COALESCE(json_extract(f.value,'$.before_hash'),'') AS before_hash,
			COALESCE(json_extract(f.value,'$.before_mode'),'') AS before_mode,
			COALESCE(json_extract(f.value,'$.after_hash'),'') AS after_hash,
			COALESCE(json_extract(f.value,'$.after_mode'),'') AS after_mode
			FROM task_artifacts a JOIN json_each(a.manifest_json,'$.files') f
			WHERE a.task_id=? ORDER BY a.revision,f.key`
		if s.isPostgres() {
			query = `SELECT a.id,f.value->>'path' AS path,
				COALESCE(f.value->>'before_hash','') AS before_hash,
				COALESCE(f.value->>'before_mode','') AS before_mode,
				COALESCE(f.value->>'after_hash','') AS after_hash,
				COALESCE(f.value->>'after_mode','') AS after_mode
				FROM task_artifacts a CROSS JOIN LATERAL jsonb_array_elements(CAST(a.manifest_json AS jsonb)->'files') WITH ORDINALITY AS f(value,position)
				WHERE a.task_id=? ORDER BY a.revision,f.position`
		}
		var files []struct {
			ID string `db:"id"`
			task.ArtifactFileEvidence
		}
		if err := tx.SelectContext(ctx, &files, s.Q(query), id); err != nil {
			return err
		}
		positions := make(map[string]int, len(out.Items))
		for i, artifact := range out.Items {
			positions[artifact.ID] = i
		}
		for _, file := range files {
			artifact := &out.Items[positions[file.ID]]
			artifact.Files = append(artifact.Files, file.ArtifactFileEvidence)
		}
		return tx.SelectContext(ctx, &out.Handoffs, s.Q("SELECT * FROM task_artifact_handoffs WHERE task_id=? ORDER BY created_at DESC,id"), id)
	})
	return out, err
}
