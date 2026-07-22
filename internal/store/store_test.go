package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestOpenMigratesLegacyCredentialProfilesWithCommitIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE credential_profiles (id TEXT PRIMARY KEY,kind TEXT NOT NULL,name TEXT NOT NULL,endpoint TEXT NOT NULL,username TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',ciphertext TEXT NOT NULL,key_version INTEGER NOT NULL DEFAULT 1,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(kind,name)); INSERT INTO credential_profiles(id,kind,name,endpoint,username,ciphertext) VALUES('legacy-git','git','GitHub','https://github.com','git-user','v1:test')`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(path + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	profile, err := database.CredentialProfile(context.Background(), "legacy-git")
	if err != nil || profile.CommitName != "" || profile.CommitEmail != "" {
		t.Fatalf("legacy profile migration failed: %#v %v", profile, err)
	}
}

func TestOpenMigratesLegacyUserToTOTPAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-user.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,totp_secret TEXT NOT NULL,recovery_hashes TEXT NOT NULL,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes) VALUES('legacy-user','admin','unused','encrypted','[]')`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(path + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	user, err := database.UserByName(context.Background(), "admin")
	if err != nil || user.AuthMode != AuthModeTOTP {
		t.Fatalf("legacy user authentication mode = %q, %v", user.AuthMode, err)
	}
}

func TestFailedCodexSnapshotRetainsCachedData(t *testing.T) {
	database := testStore(t)
	ctx := context.Background()
	result := protocol.CodexCapabilityResult{Supported: true, CodexVersion: "0.139.0", Data: json.RawMessage(`{"goal":{"objective":"ship"}}`)}
	if err := database.SaveCodexSnapshot(ctx, "thread", "thread-1", "goal", result); err != nil {
		t.Fatal(err)
	}
	if err := database.FailCodexSnapshot(ctx, "thread", "thread-1", "goal", "timeout"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.CodexSnapshot(ctx, "thread", "thread-1", "goal")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != "failed" || snapshot.Error != "timeout" || !strings.Contains(snapshot.Data, "ship") {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestOpenMigratesLegacyProjectAndThreadPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-preferences.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE projects (id TEXT PRIMARY KEY,name TEXT NOT NULL,remote_url TEXT NOT NULL DEFAULT '',normalized_remote TEXT NOT NULL DEFAULT '',created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE codex_threads (id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,codex_thread_id TEXT NOT NULL DEFAULT '',title TEXT NOT NULL DEFAULT 'New session',status TEXT NOT NULL DEFAULT 'idle',last_sequence INTEGER NOT NULL DEFAULT 0,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO projects(id,name,remote_url,normalized_remote) VALUES('legacy-project','Legacy','https://example.com/legacy.git','https://example.com/legacy');`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	database, err := Open(path + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, table := range []string{"projects", "codex_threads"} {
		var columns []string
		if err := database.DB.SelectContext(context.Background(), &columns, "SELECT name FROM pragma_table_info(?)", table); err != nil {
			t.Fatal(err)
		}
		if table == "projects" && (!containsString(columns, "pinned_at") || !containsString(columns, "hidden_at")) {
			t.Fatalf("project preference columns missing: %v", columns)
		}
		if table == "codex_threads" && (!containsString(columns, "pinned_at") || !containsString(columns, "archived_at")) {
			t.Fatalf("thread preference column missing: %v", columns)
		}
	}
	project, err := database.Project(context.Background(), "legacy-project")
	if err != nil || project.PinnedAt != nil || project.HiddenAt != nil {
		t.Fatalf("legacy project did not retain null preferences: %#v %v", project, err)
	}
	remotes, err := database.ProjectRemotes(context.Background(), project.ID)
	if err != nil || len(remotes) != 1 || remotes[0].Name != "origin" || remotes[0].FetchURL != project.RemoteURL {
		t.Fatalf("legacy imported remote was not backfilled: %#v %v", remotes, err)
	}
}

func TestCreateProjectRegistersImportedRemote(t *testing.T) {
	database := testStore(t)
	project, err := database.CreateProject(context.Background(), "imported", "https://example.com/imported.git")
	if err != nil {
		t.Fatal(err)
	}
	remotes, err := database.ProjectRemotes(context.Background(), project.ID)
	if err != nil || len(remotes) != 1 || remotes[0].Name != "origin" || remotes[0].FetchURL != project.RemoteURL || remotes[0].Mode != "existing" {
		t.Fatalf("imported project remote was not registered atomically: %#v %v", remotes, err)
	}
}

func TestArchiveAndForkThreadCopiesVisibleHistoryTransactionally(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if _, err := database.CreateEnrollment(ctx, "fork-node", []string{"/srv"}, "fork-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "fork-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "fork-node.local", "fork-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/fork", Name: "fork-repo"}}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("unexpected projects: %#v %v", projects, err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	project, workspace := projects[0], workspaces[0]
	thread, err := database.CreateThread(ctx, workspace.ID, "Original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE codex_threads SET codex_thread_id=? WHERE id=?"), "codex-original", thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "user.message", Payload: json.RawMessage(`{"text":"hello"}`)}); err != nil {
		t.Fatal(err)
	}
	command := protocol.ForkThreadCommand{SourceThreadID: thread.ID, TargetThreadID: "fork-target", WorkspaceID: workspace.ID, Title: "Continued"}
	if err := database.CommitThreadFork(ctx, command, "codex-fork"); err != nil {
		t.Fatal(err)
	}
	if err := database.CommitThreadFork(ctx, command, "codex-fork"); err != nil {
		t.Fatalf("fork commit must be idempotent: %v", err)
	}
	forked, err := database.Thread(ctx, command.TargetThreadID)
	if err != nil || forked.CodexThreadID != "codex-fork" {
		t.Fatalf("unexpected forked thread: %#v %v", forked, err)
	}
	events, err := database.ConversationEvents(ctx, forked.ID, 0, 10)
	if err != nil || len(events) != 1 || !strings.Contains(string(events[0].Payload), "hello") {
		t.Fatalf("history was not copied: %#v %v", events, err)
	}
	count, err := database.ArchiveProjectThreads(ctx, project.ID)
	if err != nil || count != 2 {
		t.Fatalf("unexpected archive result: %d %v", count, err)
	}
	active, _ := database.ListThreads(ctx)
	archived, _ := database.ListThreads(ctx, "true")
	if len(active) != 0 || len(archived) != 2 {
		t.Fatalf("unexpected archive lists: active=%d archived=%d", len(active), len(archived))
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestProjectAndThreadPreferencesAreReturnedAndSorted(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if _, err := database.CreateEnrollment(ctx, "preference-node", []string{"/srv"}, "preference-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "preference-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "preference-node.local", "preference-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{
		{Path: "/srv/a", Name: "alpha", RemoteURL: "https://example.com/alpha.git"},
		{Path: "/srv/b", Name: "beta", RemoteURL: "https://example.com/beta.git"},
	}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatalf("unexpected projects: %#v %v", projects, err)
	}
	alpha, beta := projects[0], projects[1]
	if alpha.Name != "alpha" || beta.Name != "beta" {
		t.Fatalf("unexpected initial project order: %#v", projects)
	}
	trueValue := true
	if _, err := database.UpdateProject(ctx, alpha.ID, nil, &trueValue, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateProject(ctx, beta.ID, nil, nil, &trueValue); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	var alphaWorkspace, betaWorkspace string
	for _, workspace := range workspaces {
		if workspace.ProjectID == alpha.ID {
			alphaWorkspace = workspace.ID
		} else if workspace.ProjectID == beta.ID {
			betaWorkspace = workspace.ID
		}
	}
	alphaThread, err := database.CreateThread(ctx, alphaWorkspace, "alpha thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateThread(ctx, alphaWorkspace, "alpha unpinned"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateThread(ctx, betaWorkspace, "beta thread"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateThread(ctx, alphaThread.ID, nil, &trueValue); err != nil {
		t.Fatal(err)
	}
	threads, err := database.ListThreads(ctx)
	if err != nil || len(threads) != 3 {
		t.Fatalf("unexpected threads: %#v %v", threads, err)
	}
	if threads[0].ID != alphaThread.ID || threads[1].ProjectID != alpha.ID || threads[1].PinnedAt != nil || threads[2].ProjectID != beta.ID {
		t.Fatalf("project and thread pins did not sort first: %#v", threads)
	}
	if threads[0].ProjectPinnedAt == nil || threads[0].PinnedAt == nil || threads[0].ProjectHiddenAt != nil {
		t.Fatalf("thread preference fields missing: %#v", threads[0])
	}
	var encoded map[string]any
	raw, err := json.Marshal(threads[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"pinned_at", "project_pinned_at", "project_hidden_at"} {
		if _, ok := encoded[field]; !ok {
			t.Fatalf("thread JSON missing %q: %s", field, raw)
		}
	}
	updated, err := database.UpdateProject(ctx, alpha.ID, strPtr(" Renamed "), nil, nil)
	if err != nil || updated.Name != " Renamed " {
		t.Fatalf("project update did not preserve store input: %#v %v", updated, err)
	}
	if _, err := database.UpdateThread(ctx, alphaThread.ID, strPtr(" Retitled "), nil); err != nil {
		t.Fatal(err)
	}
	projects, err = database.ListProjects(ctx)
	if err != nil || len(projects) != 2 || projects[1].ID != beta.ID || projects[1].HiddenAt == nil {
		t.Fatalf("hidden projects should remain in the project list: %#v %v", projects, err)
	}
}

func strPtr(value string) *string { return &value }

func testStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestEnrollmentInventoryAndOperations(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	token := "enrollment-token"
	if _, err := database.CreateEnrollment(ctx, "build-01", []string{"/srv"}, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "build-01.local", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	serverID, err := database.AuthenticateAgent(ctx, "agent-token")
	if err != nil || serverID != server.ID {
		t.Fatalf("agent authentication failed: %s %v", serverID, err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/app", Name: "app", RemoteURL: "https://example.com/app.git", Branch: "main", CommitSHA: "abc"}}}); err != nil {
		t.Fatal(err)
	}
	projects, err := database.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].WorkspaceCount != 1 {
		t.Fatalf("unexpected projects: %#v %v", projects, err)
	}
	remotes, err := database.ProjectRemotes(ctx, projects[0].ID)
	if err != nil || len(remotes) != 1 || remotes[0].FetchURL != "https://example.com/app.git" {
		t.Fatalf("inventory remote was not registered: %#v %v", remotes, err)
	}
	operationID, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]bool{"now": true}, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	duplicateID, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]bool{"now": true}, "scan-1")
	if err != nil || duplicateID != operationID {
		t.Fatalf("idempotency failed: %s %s %v", operationID, duplicateID, err)
	}
}

func TestRepairEnrollmentPreservesServerAndWorkspace(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if _, err := database.CreateEnrollment(ctx, "repair-node", []string{"/srv"}, "initial-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	initial, err := database.ConsumeEnrollment(ctx, "initial-enrollment")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, initial, "repair-node.local", "old-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/app", Name: "app", RemoteURL: "https://example.com/app.git", Branch: "main", CommitSHA: "abc"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRepairEnrollment(ctx, server.ID, "repair-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	repair, err := database.ConsumeEnrollment(ctx, "repair-enrollment")
	if err != nil || repair.ServerID != server.ID {
		t.Fatalf("unexpected repair enrollment: %#v %v", repair, err)
	}
	repaired, err := database.EnrollServer(ctx, repair, "repair-node-new.local", "new-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ID != server.ID {
		t.Fatalf("repair changed server ID: %s != %s", repaired.ID, server.ID)
	}
	if _, err := database.AuthenticateAgent(ctx, "old-agent-token"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old Agent credential remains valid: %v", err)
	}
	if id, err := database.AuthenticateAgent(ctx, "new-agent-token"); err != nil || id != server.ID {
		t.Fatalf("new Agent credential was not installed: %s %v", id, err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 || workspaces[0].ServerID != server.ID {
		t.Fatalf("repair changed workspace association: %#v %v", workspaces, err)
	}
}

func TestApprovalResolutionAndOperationQueueAreAtomic(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if _, err := database.CreateEnrollment(ctx, "approval-node", []string{"/srv"}, "approval-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "approval-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "approval-node.local", "approval-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "approval")
	if err != nil {
		t.Fatal(err)
	}
	approvalID := NewID()
	if _, err := database.DB.ExecContext(ctx, database.Q("INSERT INTO approvals(id,thread_id,request_id,kind,detail,status,expires_at) VALUES(?,?,?,?,?,'pending',?)"), approvalID, thread.ID, "request-1", "item/commandExecution/requestApproval", `{}`, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	command := protocol.ApprovalDecisionCommand{ThreadID: thread.ID, RequestID: "request-1", Decision: "approved"}
	if _, err := database.ResolveApprovalAndQueue(ctx, approvalID, "missing-server", "approved", command); err == nil {
		t.Fatal("expected operation foreign-key failure")
	}
	var status string
	if err := database.DB.GetContext(ctx, &status, database.Q("SELECT status FROM approvals WHERE id=?"), approvalID); err != nil || status != "pending" {
		t.Fatalf("failed queue did not roll back approval: status=%q err=%v", status, err)
	}
	operationID, err := database.ResolveApprovalAndQueue(ctx, approvalID, server.ID, "approved", command)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, operationID)
	var operationStatus string
	if statusErr := database.DB.GetContext(ctx, &operationStatus, database.Q("SELECT status FROM agent_operations WHERE id=?"), operationID); statusErr != nil {
		t.Fatal(statusErr)
	}
	if err != nil || operation.Kind != "codex.approval" || operationStatus != "queued" {
		t.Fatalf("approval operation was not queued: %#v %v", operation, err)
	}
	if err := database.DB.GetContext(ctx, &status, database.Q("SELECT status FROM approvals WHERE id=?"), approvalID); err != nil || status != "resolved" {
		t.Fatalf("approval was not resolved with operation: status=%q err=%v", status, err)
	}
}

func TestListServersUsesHeartbeatGracePeriod(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if _, err := database.CreateEnrollment(ctx, "build-01", []string{"/srv"}, "enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "build-01.local", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET status='offline',last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-30*time.Second), server.ID); err != nil {
		t.Fatal(err)
	}
	servers, err := database.ListServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Status != "online" {
		t.Fatalf("recent heartbeat should keep server online: %#v %v", servers, err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE servers SET status='online',last_seen_at=? WHERE id=?"), time.Now().UTC().Add(-2*time.Minute), server.ID); err != nil {
		t.Fatal(err)
	}
	servers, err = database.ListServers(ctx)
	if err != nil || len(servers) != 1 || servers[0].Status != "offline" {
		t.Fatalf("stale heartbeat should mark server offline: %#v %v", servers, err)
	}
}

func TestMetricsStoreCountersAboveInt32(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	if _, err := database.CreateEnrollment(ctx, "metrics-01", []string{"/srv"}, "metrics-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "metrics-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "metrics-01.local", "metrics-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	metric := protocol.Metrics{NetRxBytes: 3_000_000_000, NetTxBytes: 4_000_000_000}
	if err := database.SaveMetrics(ctx, server.ID, metric); err != nil {
		t.Fatal(err)
	}
	points, err := database.Metrics(ctx, server.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil || len(points) != 1 {
		t.Fatalf("unexpected metric points: %#v %v", points, err)
	}
	if points[0].NetRxBytes != metric.NetRxBytes || points[0].NetTxBytes != metric.NetTxBytes {
		t.Fatalf("large network counters changed: %#v", points[0])
	}
}

func TestServerMetadataFollowsEnrollmentAndCanBeUpdated(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	metadata := ServerMetadata{Address: "192.0.2.10", Configuration: "4 vCPU / 8 GB RAM", Notes: "Production API"}
	if _, err := database.CreateEnrollmentWithMetadata(ctx, "build-01", []string{"/srv"}, "metadata-token", time.Now().Add(time.Hour), metadata); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "metadata-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "build-01.local", "metadata-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	servers, err := database.ListServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("unexpected server list: %#v %v", servers, err)
	}
	if servers[0].Address != metadata.Address || servers[0].Configuration != metadata.Configuration || servers[0].Notes != metadata.Notes {
		t.Fatalf("enrollment metadata was not persisted: %#v", servers[0])
	}
	updated := ServerMetadata{Address: "server.example.com", Configuration: "8 vCPU / 16 GB RAM", Notes: "Primary production API"}
	ok, err := database.UpdateServerMetadata(ctx, server.ID, updated)
	if err != nil || !ok {
		t.Fatalf("could not update metadata: ok=%v err=%v", ok, err)
	}
	servers, err = database.ListServers(ctx)
	if err != nil || servers[0].Address != updated.Address || servers[0].Configuration != updated.Configuration || servers[0].Notes != updated.Notes {
		t.Fatalf("metadata update was not returned: %#v %v", servers, err)
	}
	if ok, err = database.UpdateServerMetadata(ctx, "missing-server", updated); err != nil || ok {
		t.Fatalf("missing server update should not succeed: ok=%v err=%v", ok, err)
	}
}

func TestEventsHaveMonotonicSequence(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	first, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: "thread", Kind: "one", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: "thread", Kind: "two", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("unexpected sequence: %d, %d", first.Sequence, second.Sequence)
	}
	replayed, err := database.AddEvent(ctx, protocol.StreamEvent{EventID: first.EventID, StreamID: first.StreamID, Kind: first.Kind, Payload: first.Payload})
	if err != nil || replayed.Sequence != first.Sequence {
		t.Fatalf("replayed event was not deduplicated: %#v %v", replayed, err)
	}
	events, err := database.Events(ctx, "thread", 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("replayed event created a duplicate: %#v %v", events, err)
	}
}

func TestConversationEventsAreNotBlockedByRawEventWindow(t *testing.T) {
	ctx := context.Background()
	database := testStore(t)
	tx, err := database.DB.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for sequence := 1; sequence <= 1005; sequence++ {
		if _, err := tx.ExecContext(ctx, database.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), "thread", sequence, "codex.item.agentMessage.delta", now, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	for index, event := range []struct{ kind, payload string }{
		{"user.message", `{"text":"visible prompt"}`},
		{"codex.item.completed", `{"item":{"type":"agentMessage","text":"visible answer"}}`},
		{"codex.turn.completed", `{"turn":{"status":"completed"}}`},
	} {
		if _, err := tx.ExecContext(ctx, database.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), "thread", 1006+index, event.kind, now, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	conversation, err := database.ConversationEvents(ctx, "thread", 0, 10000)
	if err != nil || len(conversation) != 3 || conversation[0].Sequence != 1006 || conversation[2].Sequence != 1008 {
		t.Fatalf("conversation events were hidden by raw deltas: %#v %v", conversation, err)
	}
	recent, err := database.RecentEvents(ctx, "thread", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1000 {
		t.Fatalf("unexpected recent raw window length: %d", len(recent))
	}
	if recent[0].Sequence != 9 || recent[999].Sequence != 1008 {
		t.Fatalf("unexpected recent raw window: first=%#v last=%#v", recent[0], recent[999])
	}
}
