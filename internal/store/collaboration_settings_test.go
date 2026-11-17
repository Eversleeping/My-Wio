package store

import (
	"context"
	"sync"
	"testing"

	"github.com/wio-platform/wio/internal/sessiontools"
)

func TestGlobalCollaborationCoversExistingAndNewSessions(t *testing.T) {
	assertGlobalCollaborationCoversExistingAndNewSessions(t, testStore(t))
}

func assertGlobalCollaborationCoversExistingAndNewSessions(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	refs := []sessiontools.Ref{}
	for _, engine := range []string{"codex", "claude", "gemini"} {
		refs = append(refs, newCollaborationFixture(t, db, engine).source)
	}
	global := sessiontools.Grant{Enabled: true, Worktree: true, TargetModes: map[string]string{"codex": "on-request", "claude": "default", "gemini": "default"}}
	if err := db.SetCollaborationSettings(ctx, global); err != nil {
		t.Fatal(err)
	}
	newer := newCollaborationFixture(t, db, "codex")
	refs = append(refs, newer.source)
	for _, ref := range refs {
		if err := db.SetCollaborationGrant(ctx, ref, sessiontools.Grant{}); err != nil {
			t.Fatal(err)
		}
		grant, version, err := db.collaborationGrant(ctx, db.DB, ref.Key())
		if err != nil || !grant.Enabled || !grant.Worktree || version >= 0 {
			t.Fatalf("global grant not effective for %s: %+v version=%d err=%v", ref.Key(), grant, version, err)
		}
	}

	// A globally enabled source created by the browser has no local grant row.
	// Concurrent calls must still serialize creation and replay by request ID.
	if _, err := db.DB.ExecContext(ctx, db.Q("DELETE FROM session_tool_grants WHERE source_key=?"), newer.source.Key()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	replies := make(chan sessiontools.Reply, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies <- newer.call(t, "wio_create_session", sessiontools.Params{RequestID: "global-concurrent", Engine: "claude", Prompt: "synthetic task"})
		}()
	}
	wg.Wait()
	close(replies)
	id := ""
	for reply := range replies {
		if reply.Error != nil || reply.Session == nil {
			t.Fatalf("concurrent global creation: %+v", reply)
		}
		if id != "" && id != reply.Session.ID {
			t.Fatal("global creation was duplicated")
		}
		id = reply.Session.ID
	}
	global.Enabled = false
	if err := db.SetCollaborationSettings(ctx, global); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if err := db.SetCollaborationGrant(ctx, ref, sessiontools.Grant{Enabled: true}); err != nil {
			t.Fatal(err)
		}
		grant, err := db.CollaborationGrant(ctx, ref)
		if err != nil || grant.Enabled {
			t.Fatalf("legacy grant bypassed global switch: %+v %v", grant, err)
		}
	}
	reply := newer.call(t, "wio_create_session", sessiontools.Params{RequestID: "global-disabled", Engine: "codex", DirectoryMode: "shared"})
	if reply.Error == nil {
		t.Fatalf("creation allowed while globally disabled: %+v", reply)
	}
}

func TestGlobalCollaborationVersionsAndDelegation(t *testing.T) {
	ctx := context.Background()
	f := newCollaborationFixture(t, testStore(t), "codex")
	global := sessiontools.Grant{Enabled: true, Delegate: true, TargetModes: map[string]string{"claude": "default"}}
	if err := f.db.SetCollaborationSettings(ctx, global); err != nil {
		t.Fatal(err)
	}
	_, initial, _ := f.db.collaborationGrant(ctx, f.db.DB, f.source.Key())
	reply := f.call(t, "wio_create_session", sessiontools.Params{RequestID: "global-child", Engine: "claude", DirectoryMode: "shared"})
	if reply.Error != nil || reply.Session == nil {
		t.Fatalf("create: %+v", reply)
	}
	child, err := f.db.CollaborationGrant(ctx, *reply.Session)
	if err != nil || !child.Enabled {
		t.Fatalf("child: %+v %v", child, err)
	}
	global.ShowCreationResults = true
	if err = f.db.SetCollaborationSettings(ctx, global); err != nil {
		t.Fatal(err)
	}
	child, afterDisplay, err := f.db.collaborationGrant(ctx, f.db.DB, reply.Session.Key())
	if err != nil || !child.ShowCreationResults || afterDisplay != initial {
		t.Fatalf("display invalidated authorization: %+v %d %d %v", child, initial, afterDisplay, err)
	}
	global.Delegate = false
	if err = f.db.SetCollaborationSettings(ctx, global); err != nil {
		t.Fatal(err)
	}
	child, afterScope, err := f.db.collaborationGrant(ctx, f.db.DB, reply.Session.Key())
	if err != nil || child.Enabled || afterScope == initial {
		t.Fatalf("delegation not revoked: %+v %v", child, err)
	}
	root, _ := f.db.CollaborationGrant(ctx, f.source)
	if !root.Enabled {
		t.Fatal("revoking delegation disabled the root")
	}
}

func TestGlobalCollaborationValidatesScope(t *testing.T) {
	db := testStore(t)
	for _, grant := range []sessiontools.Grant{
		{Enabled: true, WorkspaceIDs: []string{"workspace"}},
		{Enabled: true, SessionRefs: []sessiontools.Ref{{Engine: "codex", ID: "other"}}},
		{Enabled: true, TargetModes: map[string]string{"claude": "never"}},
	} {
		if err := db.SetCollaborationSettings(context.Background(), grant); err == nil {
			t.Fatalf("accepted invalid global grant: %+v", grant)
		}
	}
}
