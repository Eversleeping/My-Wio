package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/wio-platform/wio/internal/session"
	"testing"
	"time"
)

func TestAISessionReadModel(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	refs := []session.Ref{}
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		f := newCollaborationFixture(t, db, engine)
		refs = append(refs, f.source)
		got, err := db.AISession(ctx, f.source)
		if err != nil {
			t.Fatal(err)
		}
		if got.Ref() != f.source || got.WorkspaceID != f.workspace || got.ServerID != f.server || got.ProjectName != "Collaboration" || got.ServerStatus != "online" || !got.Capabilities.Turns {
			t.Fatalf("read: %+v", got)
		}
		if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE workspaces SET display_name=? WHERE id=?"), "group_%", f.workspace); err != nil {
			t.Fatal(err)
		}
		task, err := db.CreateScheduledTask(ctx, ScheduledTaskInput{AgentKind: engine, ThreadID: f.source.ID, Name: "Task", Prompt: "p", Schedule: "@daily", Timezone: "UTC", Enabled: true}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE servers SET revoked_at=? WHERE id=?"), time.Now().UTC(), f.server); err != nil {
			t.Fatal(err)
		}
		if _, err = db.AISession(ctx, f.source); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("revoked visible: %v", err)
		}
		history, err := db.ScheduledTask(ctx, task.ID)
		if err != nil || history.ServerStatus != "offline" || history.ProjectName != "Collaboration" {
			t.Fatalf("history: %+v %v", history, err)
		}
		if _, err = db.DB.ExecContext(ctx, db.Q("UPDATE servers SET revoked_at=NULL WHERE id=?"), f.server); err != nil {
			t.Fatal(err)
		}
	}
	active := false
	for _, engine := range []string{"", session.Codex, session.Gemini, session.Claude} {
		got, err := db.AISessions(ctx, AISessionFilter{Engine: engine, Archived: &active, Query: "group_%"})
		want := 1
		if engine == "" {
			want = 3
		}
		if err != nil || len(got) != want {
			t.Fatalf("filtered %s: %d %v", engine, len(got), err)
		}
	}
	if _, err := db.AISessions(ctx, AISessionFilter{Engine: "unknown"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("engine: %v", err)
	}
	if _, err := db.AISessions(ctx, AISessionFilter{Limit: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("limit: %v", err)
	}
	// Archive before applying the limit, and verify a literal wildcard is escaped.
	if _, err := db.DB.ExecContext(ctx, db.Q("UPDATE codex_threads SET archived_at=? WHERE id=?"), time.Now().UTC(), refs[0].ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.AISessions(ctx, AISessionFilter{Archived: &active, Limit: 1})
	if err != nil || len(got) != 1 || got[0].Engine == session.Codex {
		t.Fatalf("active limit: %+v %v", got, err)
	}
	got, err = db.AISessions(ctx, AISessionFilter{Query: "groupX"})
	if err != nil || len(got) != 0 {
		t.Fatalf("literal search: %+v %v", got, err)
	}
}
