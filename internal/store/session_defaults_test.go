package store

import (
	"context"
	"testing"
	"time"
)

func TestWorkspaceSessionDefaults(t *testing.T) { assertWorkspaceSessionDefaults(t, testStore(t)) }
func assertWorkspaceSessionDefaults(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	f := newCollaborationFixture(t, db, "codex")
	first, err := db.CreateGeminiThread(ctx, f.workspace, "flash", "yolo", "high")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateGeminiThread(ctx, f.workspace, "flash-new", "default", "low")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateClaudeThread(ctx, f.workspace, "sonnet", "acceptEdits", "max"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET pinned_at=? WHERE id=?"), time.Now().UTC(), first.ID); err != nil {
		t.Fatal(err)
	}
	other := newCollaborationFixture(t, db, "gemini")
	got, err := db.WorkspaceSessionDefaults(ctx, f.workspace)
	if err != nil || len(got) != 3 || got["gemini"].ID != first.ID || got["gemini"].Mode != "yolo" || got["gemini"].Effort != "high" || got["claude"].Mode != "acceptEdits" || got["claude"].Effort != "max" {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	otherDefaults, err := db.WorkspaceSessionDefaults(ctx, other.workspace)
	if err != nil || len(otherDefaults) != 1 || otherDefaults["gemini"].ID != other.source.ID {
		t.Fatalf("cross-workspace defaults: %+v %v", otherDefaults, err)
	}
	if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE gemini_threads SET archived_at=? WHERE id=?"), time.Now().UTC(), first.ID); err != nil {
		t.Fatal(err)
	}
	got, err = db.WorkspaceSessionDefaults(ctx, f.workspace)
	if err != nil || got["gemini"].ID != second.ID {
		t.Fatalf("archived default: %+v %v", got, err)
	}
	got, err = db.WorkspaceSessionDefaults(ctx, "missing")
	if err != nil || len(got) != 0 {
		t.Fatalf("missing workspace defaults: %+v %v", got, err)
	}
}
