package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestProjectServerPreferencesIsolation(t *testing.T) {
	assertProjectServerPreferencesIsolation(t, testStore(t))
}

func assertProjectServerPreferencesIsolation(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	servers := []Server{enrollProjectImportTestServer(t, database, "scope-a-"+NewID()), enrollProjectImportTestServer(t, database, "scope-b-"+NewID())}
	remote := "https://example.com/scoped-" + NewID() + ".git"
	var codex []Thread
	var gemini []GeminiThread
	var projectID string
	for _, server := range servers {
		if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/same", Name: "same", RemoteURL: remote}}}); err != nil {
			t.Fatal(err)
		}
		workspaces, err := database.ListWorkspaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var selected Workspace
		for _, workspace := range workspaces {
			if workspace.ServerID == server.ID && workspace.Path == "/srv/same" {
				selected = workspace
			}
		}
		if selected.ID == "" {
			t.Fatal("missing workspace")
		}
		if projectID != "" && projectID != selected.ProjectID {
			t.Fatal("fixture did not share project")
		}
		projectID = selected.ProjectID
		thread, err := database.CreateThread(ctx, selected.ID, "scoped Codex")
		if err != nil {
			t.Fatal(err)
		}
		codex = append(codex, thread)
		g, err := database.CreateGeminiThread(ctx, selected.ID, "flash", "default")
		if err != nil {
			t.Fatal(err)
		}
		gemini = append(gemini, g)
	}
	yes, no := true, false
	if _, err := database.UpdateProjectServerPreferences(ctx, projectID, servers[0].ID, nil, &yes, nil); err != nil {
		t.Fatal(err)
	}
	check := func(wantA, wantB bool) {
		t.Helper()
		for i, want := range []bool{wantA, wantB} {
			thread, err := database.Thread(ctx, codex[i].ID)
			g, gerr := database.GeminiThread(ctx, gemini[i].ID)
			if err != nil || gerr != nil || (thread.ProjectPinnedAt != nil) != want || (g.ProjectPinnedAt != nil) != want {
				t.Fatalf("server %d pin leaked: Codex=%#v Gemini=%#v errors=%v/%v", i, thread, g, err, gerr)
			}
		}
	}
	check(true, false)
	page, err := database.ListThreadsPage(ctx, "", 1, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != codex[0].ID || !page.HasMore {
		t.Fatalf("scoped pin pagination: %#v %v", page, err)
	}
	if _, err := database.UpdateProjectServerPreferences(ctx, projectID, servers[1].ID, nil, &yes, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateProjectServerPreferences(ctx, projectID, servers[0].ID, nil, &no, nil); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	project, err := database.Project(ctx, projectID)
	if err != nil || project.PinnedAt != nil {
		t.Fatalf("shared project was changed: %#v %v", project, err)
	}
	name := "Only server A"
	if _, err := database.UpdateProjectServerPreferences(ctx, projectID, servers[0].ID, &name, nil, &yes); err != nil {
		t.Fatal(err)
	}
	for i := range servers {
		c, err := database.Thread(ctx, codex[i].ID)
		g, gerr := database.GeminiThread(ctx, gemini[i].ID)
		if err != nil || gerr != nil || (c.ProjectHiddenAt != nil) != (i == 0) || (g.ProjectHiddenAt != nil) != (i == 0) || (c.ProjectName == name) != (i == 0) || (g.ProjectName == name) != (i == 0) {
			t.Fatalf("server rename/hide leaked: %#v %#v %v %v", c, g, err, gerr)
		}
	}
	if _, err := database.UpdateProject(ctx, projectID, nil, nil, &no); err != nil {
		t.Fatal(err)
	}
	shown, err := database.Thread(ctx, codex[0].ID)
	if err != nil || shown.ProjectHiddenAt != nil {
		t.Fatal("global project management could not restore hidden sidebar project")
	}
	for _, archive := range []func(context.Context, string, ...string) (int64, error){database.ArchiveProjectThreads, database.ArchiveGeminiProject} {
		count, err := archive(ctx, projectID, servers[0].ID)
		if err != nil || count != 1 {
			t.Fatalf("scoped archive: %d %v", count, err)
		}
	}
	b, err := database.Thread(ctx, codex[1].ID)
	gb, gerr := database.GeminiThread(ctx, gemini[1].ID)
	if err != nil || gerr != nil || b.ArchivedAt != nil || gb.ArchivedAt != nil {
		t.Fatal("archive affected the other server")
	}
	if _, err := database.UpdateProjectServerPreferences(ctx, projectID, "missing", nil, &yes, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid server accepted: %v", err)
	}
}

func TestProjectServerPreferencesOverrideLegacyAndSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "preferences.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	server := enrollProjectImportTestServer(t, database, "persistent")
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/legacy", Name: "legacy"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatal(err)
	}
	yes, no := true, false
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateProject(ctx, workspaces[0].ProjectID, nil, &yes, &yes); err != nil {
		t.Fatal(err)
	}
	thread, err = database.Thread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.ProjectPinnedAt == nil || thread.ProjectHiddenAt == nil {
		t.Fatal("legacy preferences were lost")
	}
	if _, err := database.UpdateProjectServerPreferences(ctx, thread.ProjectID, server.ID, nil, &no, &no); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	thread, err = database.Thread(ctx, thread.ID)
	if err != nil || thread.ProjectPinnedAt != nil || thread.ProjectHiddenAt != nil {
		t.Fatalf("legacy preferences overrode explicit clears: %#v %v", thread, err)
	}
}

func TestHiddenServerProjectsRecovery(t *testing.T) {
	assertHiddenServerProjectsRecovery(t, testStore(t))
}

func assertHiddenServerProjectsRecovery(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	servers := []Server{enrollProjectImportTestServer(t, database, "hidden-a-"+NewID()), enrollProjectImportTestServer(t, database, "hidden-b-"+NewID())}
	remote := "https://example.com/hidden-" + NewID() + ".git"
	var workspaces []Workspace
	var oldCodex []Thread
	var oldGemini []GeminiThread
	for _, server := range servers {
		if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{
			{Path: "/srv/hidden", Name: "hidden", RemoteURL: remote},
			{Path: "/srv/hidden-copy", Name: "hidden", RemoteURL: remote},
		}}); err != nil {
			t.Fatal(err)
		}
		all, err := database.ListWorkspaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, ws := range all {
			if ws.ServerID != server.ID || ws.Path != "/srv/hidden" {
				continue
			}
			workspaces = append(workspaces, ws)
			c, err := database.CreateThread(ctx, ws.ID, "old Codex")
			if err != nil {
				t.Fatal(err)
			}
			g, err := database.CreateGeminiThread(ctx, ws.ID, "flash", "default")
			if err != nil {
				t.Fatal(err)
			}
			oldCodex, oldGemini = append(oldCodex, c), append(oldGemini, g)
		}
	}
	if len(workspaces) != 2 || workspaces[0].ProjectID != workspaces[1].ProjectID {
		t.Fatal("invalid shared-project fixture")
	}
	projectID := workspaces[0].ProjectID
	yes, no := true, false
	setHidden := func() {
		t.Helper()
		for _, server := range servers {
			name := "Hidden on " + server.Name
			if _, err := database.UpdateProjectServerPreferences(ctx, projectID, server.ID, &name, &yes, &yes); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(hiddenA, hiddenB bool) {
		t.Helper()
		items, err := database.HiddenServerProjects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, item := range items {
			if item.ProjectID == projectID {
				seen[item.ServerID]++
				if item.ProjectName != "Hidden on "+item.ServerName {
					t.Fatalf("wrong hidden project label: %#v", item)
				}
			}
		}
		for i, want := range []bool{hiddenA, hiddenB} {
			if (seen[servers[i].ID] == 1) != want || seen[servers[i].ID] > 1 {
				t.Fatalf("hidden list not distinct or wrong scope: %#v", seen)
			}
			c, err := database.Thread(ctx, oldCodex[i].ID)
			g, gerr := database.GeminiThread(ctx, oldGemini[i].ID)
			if err != nil || gerr != nil || (c.ProjectHiddenAt != nil) != want || (g.ProjectHiddenAt != nil) != want || c.ProjectPinnedAt == nil || g.ProjectPinnedAt == nil || c.ProjectName != "Hidden on "+servers[i].Name || g.ProjectName != c.ProjectName {
				t.Fatalf("visibility/name/pin mismatch: %#v %#v %v %v", c, g, err, gerr)
			}
		}
	}
	setHidden()
	check(true, true)
	if _, err := database.UpdateProjectServerPreferences(ctx, projectID, servers[0].ID, nil, nil, &no); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	for _, kind := range []string{"codex", "gemini"} {
		setHidden()
		if kind == "codex" {
			c, err := database.CreateThread(ctx, workspaces[0].ID, "new Codex")
			if err != nil || c.ProjectHiddenAt != nil {
				t.Fatalf("new Codex still hidden: %#v %v", c, err)
			}
		} else {
			g, err := database.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default")
			if err != nil || g.ProjectHiddenAt != nil {
				t.Fatalf("new Gemini still hidden: %#v %v", g, err)
			}
		}
		check(false, true)
	}
	setHidden()
	if _, err := database.CreateThread(ctx, "missing-workspace", "invalid"); err == nil {
		t.Fatal("invalid Codex workspace accepted")
	}
	if _, err := database.CreateGeminiThread(ctx, "missing-workspace", "flash", "default"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid Gemini workspace: %v", err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET status='missing' WHERE id=?"), workspaces[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unavailable Gemini workspace: %v", err)
	}
	check(true, true)
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE workspaces SET status='ready' WHERE id=?"), workspaces[0].ID); err != nil {
		t.Fatal(err)
	}
	// Legacy global hides must become an explicit show only on the selected server.
	for _, kind := range []string{"codex", "gemini"} {
		if _, err := database.DB.ExecContext(ctx, database.Q("DELETE FROM project_server_preferences WHERE project_id=?"), projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := database.UpdateProject(ctx, projectID, nil, &yes, &yes); err != nil {
			t.Fatal(err)
		}
		if kind == "codex" {
			if _, err := database.CreateThread(ctx, workspaces[0].ID, "legacy show"); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := database.CreateGeminiThread(ctx, workspaces[0].ID, "flash", "default"); err != nil {
				t.Fatal(err)
			}
		}
		for i, want := range []bool{false, true} {
			p, err := database.ProjectOnServer(ctx, projectID, servers[i].ID)
			if err != nil || (p.HiddenAt != nil) != want || p.PinnedAt == nil {
				t.Fatalf("legacy hide leaked: %#v %v", p, err)
			}
		}
	}
}
