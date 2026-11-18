package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/task"
	"github.com/wio-platform/wio/internal/taskartifact"
)

func TestTaskArtifactsStorage(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "artifacts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertTaskArtifactsStorage(t, db)
}

func assertTaskArtifactsStorage(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f, created, plan, run := newTaskExecution(t, db)
	if _, err := db.DispatchTaskStep(ctx, created.ID, run.ID, "a", "worker"); err != nil {
		t.Fatal(err)
	}
	attempt := taskAttemptFor(t, db, run.ID, "a")
	taskFinish(t, db, attempt, "succeeded")
	d, err := db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	capture := task.CaptureArtifact{Meta: task.Meta{Actor: "task-user", RequestID: "capture-review", ExpectedVersion: d.Task.Version}, AttemptID: attempt.ID, Paths: []string{"notes.txt"}}
	a, err := db.CaptureTaskArtifact(ctx, f.project, created.ID, capture)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := db.CaptureTaskArtifact(ctx, f.project, created.ID, capture)
	if err != nil || replay.ID != a.ID || replay.OperationID != a.OperationID {
		t.Fatal("duplicated capture", err)
	}
	data := []byte("private selected artifact contents")
	manifest := protocol.ArtifactManifest{BaseSHA: strings.Repeat("1", 40), Files: []protocol.ArtifactFile{{Path: "notes.txt", AfterHash: taskartifact.Hash(data), AfterMode: "100644", Content: data}}}
	raw, _ := json.Marshal(manifest)
	op, err := db.Operation(ctx, a.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteTaskArtifact(ctx, op, protocol.OperationResult{OperationID: op.ID, Status: "succeeded", Data: raw}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneRetainedData(ctx, time.Now().UTC().Add(48*time.Hour), RetentionPolicy{OperationHistory: time.Hour}); err != nil {
		t.Fatal("retained artifact prevented pruning", err)
	}
	if _, err := db.Operation(ctx, a.OperationID); err != nil {
		t.Fatal("artifact lost its capture operation", err)
	}

	digest, _ := taskartifact.Validate(manifest)
	result, err := db.TaskArtifacts(ctx, f.project, created.ID)
	if err != nil || len(result.Items) != 1 || result.Items[0].Digest != digest {
		t.Fatal(result, err)
	}
	metadata, _ := json.Marshal(result)
	if strings.Contains(string(metadata), "content") {
		t.Fatal("returned artifact file bytes with metadata")
	}
	target := plan.Members[1].WorkspaceID
	destination := newTaskFixture(t, db)
	if _, err := db.DB.Exec(db.Q("UPDATE workspaces SET server_id=? WHERE id=?"), destination.server, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(db.Q("UPDATE servers SET agent_version='0.3.41',last_seen_at=CURRENT_TIMESTAMP WHERE id=?"), destination.server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(db.Q("UPDATE workspaces SET kind='worktree', management_mode='managed' WHERE id=?"), target); err != nil {
		t.Fatal(err)
	}
	d, err = db.TaskDetail(ctx, f.project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := task.HandoffArtifact{Meta: task.Meta{Actor: "task-user", RequestID: "handoff-for-review", ExpectedVersion: d.Task.Version}, WorkspaceID: target, Action: "apply", Digest: "incorrect"}
	_, err = db.HandoffTaskArtifact(ctx, f.project, created.ID, a.ID, input)
	taskCode(t, err, task.Conflict)
	input.Digest = digest
	_, err = db.HandoffTaskArtifact(ctx, f.project, created.ID, a.ID, input)
	taskCode(t, err, task.Unsupported)
	if _, err := db.DB.Exec(db.Q("INSERT INTO server_capabilities(server_id,capabilities) SELECT ?,capabilities FROM server_capabilities WHERE server_id=?"), destination.server, f.server); err != nil {
		t.Fatal(err)
	}
	h, err := db.HandoffTaskArtifact(ctx, f.project, created.ID, a.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	op, err = db.Operation(ctx, h.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var cmd protocol.TaskArtifactHandoff
	if err := json.Unmarshal([]byte(op.Payload), &cmd); err != nil || op.ServerID != destination.server || cmd.Manifest.Files[0].AfterHash != manifest.Files[0].AfterHash || cmd.Workspace == "" {
		t.Fatal("lost immutable handoff", err)
	}
}
