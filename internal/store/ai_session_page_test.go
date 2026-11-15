package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/session"
)

func TestAISessionPagination(t *testing.T) { assertAISessionPagination(t, testStore(t)) }
func assertAISessionPagination(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	token := "cursorfixture_" + NewID()
	at := time.Date(2026, 11, 15, 1, 2, 3, 456789000, time.UTC)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.DB.ExecContext(ctx, db.Q(query), args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		f := newCollaborationFixture(t, db, engine)
		for n := 0; n < 3; n++ {
			var id string
			switch engine {
			case session.Codex:
				v, e := db.CreateThread(ctx, f.workspace, "other")
				if e != nil {
					t.Fatal(e)
				}
				id = v.ID
			case session.Gemini:
				v, e := db.CreateGeminiThread(ctx, f.workspace, "auto", "default")
				if e != nil {
					t.Fatal(e)
				}
				id = v.ID
			case session.Claude:
				v, e := db.CreateClaudeThread(ctx, f.workspace, "sonnet", "default", "")
				if e != nil {
					t.Fatal(e)
				}
				id = v.ID
			}
			if n == 0 {
				exec("UPDATE "+engine+"_threads SET archived_at=? WHERE id=?", at, id)
			}
			if n == 1 {
				if engine == session.Claude {
					exec("INSERT INTO claude_thread_management(thread_id,pinned_at) VALUES(?,?)", id, at)
				} else {
					exec("UPDATE "+engine+"_threads SET pinned_at=? WHERE id=?", at, id)
				}
			}
		}
		exec("UPDATE "+engine+"_threads SET title=?,updated_at=? WHERE workspace_id=?", token, at, f.workspace)
		if i < 2 {
			exec("UPDATE projects SET pinned_at=? WHERE id=(SELECT project_id FROM workspaces WHERE id=?)", at, f.workspace)
		}
	}
	active := false
	filter := session.Filter{Query: token, Archived: &active, Limit: 2}
	expected, err := db.AISessions(ctx, AISessionFilter{Query: token, Archived: &active})
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) != 9 {
		t.Fatal(len(expected))
	}
	if expected[0].ProjectPinnedAt == nil || expected[0].PinnedAt == nil || expected[6].ProjectPinnedAt != nil {
		t.Fatalf("pin priority: %+v", expected)
	}
	all := []string{}
	var groups []session.Group
	for n := 0; n < 10; n++ {
		page, err := db.AISessionPage(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatal(page)
		}
		if n == 0 {
			groups = page.Groups
		}
		if !reflect.DeepEqual(page.Groups, groups) || len(groups) != 3 {
			t.Fatalf("groups changed with page: %+v", page.Groups)
		}
		for _, g := range page.Groups {
			if g.MatchedCount != 3 || !g.LatestUpdatedAt.Equal(at) {
				t.Fatalf("group: %+v", g)
			}
		}
		for _, item := range page.Items {
			all = append(all, item.Ref().Key())
		}
		if page.NextCursor == nil {
			break
		}
		filter.Cursor = *page.NextCursor
		if n == 9 {
			t.Fatal("cursor did not terminate")
		}
	}
	want := []string{}
	for _, item := range expected {
		want = append(want, item.Ref().Key())
	}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("keyset order/ties: got %v want %v", all, want)
	}
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		for _, archived := range []*bool{&active, func() *bool { x := true; return &x }(), nil} {
			page, e := db.AISessionPage(ctx, session.Filter{Engine: engine, Archived: archived, Query: token, Limit: 1})
			count := 3
			if archived == nil {
				count = 4
			} else if *archived {
				count = 1
			}
			if e != nil || len(page.Items) != 1 || len(page.Groups) != 1 || page.Groups[0].MatchedCount != count {
				t.Fatalf("filter %s %v: %+v %v", engine, archived, page, e)
			}
		}
	}
	// The cursor's values survive deletion of the anchor and insertion before it.
	filter.Cursor = ""
	first, err := db.AISessionPage(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	anchor := first.Items[1]
	exec("DELETE FROM "+anchor.Engine+"_threads WHERE id=?", anchor.ID)
	inserted, e := db.CreateThread(ctx, first.Items[0].WorkspaceID, token)
	if e != nil {
		t.Fatal(e)
	}
	exec("UPDATE codex_threads SET pinned_at=?,updated_at=? WHERE id=?", at.Add(time.Hour), at.Add(time.Hour), inserted.ID)
	filter.Cursor = *first.NextCursor
	next, err := db.AISessionPage(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if next.Items[0].Ref() != expected[2].Ref() {
		t.Fatalf("deleted anchor: %+v", next)
	}
	bad := filter
	bad.Engine = session.Claude
	if _, e := db.AISessionPage(ctx, bad); !errors.Is(e, ErrInvalidInput) {
		t.Fatalf("cursor/filter mismatch: %v", e)
	}
	for _, cursor := range []string{"broken", "e30", fmt.Sprintf("%05000d", 1)} {
		bad = filter
		bad.Cursor = cursor
		if _, e := db.AISessionPage(ctx, bad); !errors.Is(e, ErrInvalidInput) {
			t.Fatalf("bad cursor: %v", e)
		}
	}
	empty, e := db.AISessionPage(ctx, session.Filter{Query: token + "missing"})
	if e != nil || empty.Items == nil || empty.Groups == nil || empty.NextCursor != nil {
		t.Fatalf("empty: %+v %v", empty, e)
	}
}
