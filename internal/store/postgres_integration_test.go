//go:build postgresintegration

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestPostgresMigrationAndStorage(t *testing.T) {
	port := availablePostgresPort(t)
	root := t.TempDir()
	var postgresLog bytes.Buffer
	config := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(port).
		Database("wio_test").
		Username("wio_test").
		Password("wio_test").
		RuntimePath(filepath.Join(root, "runtime")).
		DataPath(filepath.Join(root, "data")).
		StartTimeout(60 * time.Second).
		Logger(&postgresLog)
	postgres := embeddedpostgres.NewDatabase(config)
	if err := postgres.Start(); err != nil {
		t.Fatalf("start embedded PostgreSQL: %v\n%s", err, postgresLog.String())
	}
	t.Cleanup(func() {
		if err := postgres.Stop(); err != nil {
			t.Errorf("stop embedded PostgreSQL: %v\n%s", err, postgresLog.String())
		}
	})

	databaseURL := config.GetConnectionURL() + "?sslmode=disable"
	createLegacyPostgresSchema(t, databaseURL)

	for attempt := 1; attempt <= 2; attempt++ {
		database, err := Open(databaseURL)
		if err != nil {
			t.Fatalf("open migrated PostgreSQL attempt %d: %v", attempt, err)
		}
		assertPostgresMigrationAndStorage(t, database, attempt == 1)
		if attempt == 1 {
			if _, err := database.DB.ExecContext(context.Background(), database.Q("UPDATE servers SET managed_roots=? WHERE id=?"), `["/srv/managed"]`, "legacy-server"); err != nil {
				t.Fatal(err)
			}
			if err := database.UpsertInventory(context.Background(), "legacy-server", protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/managed/inventory", Name: "managed-inventory", Branch: "main", CommitSHA: "abc123"}}}); err != nil {
				t.Fatal(err)
			}
			var inventoryMode string
			if err := database.DB.GetContext(context.Background(), &inventoryMode, database.Q("SELECT management_mode FROM workspaces WHERE server_id=? AND path=?"), "legacy-server", "/srv/managed/inventory"); err != nil || inventoryMode != "managed" {
				t.Fatalf("PostgreSQL inventory did not classify the managed path: %q %v", inventoryMode, err)
			}
			if _, err := database.DB.ExecContext(context.Background(), database.Q(`INSERT INTO workspaces(id,project_id,server_id,path,management_mode,kind,parent_workspace_id,branch,commit_sha) VALUES(?,?,?,?,?,'worktree',?,?,?)`), "legacy-worktree", "legacy-project", "legacy-server", "/srv/legacy-feature", "observed", "legacy-workspace", "feature/test", "abc123"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.DB.ExecContext(context.Background(), database.Q(`INSERT INTO workspaces(id,project_id,server_id,path,management_mode,kind,branch,commit_sha) VALUES(?,?,?,?,?,'primary',?,?)`), "legacy-managed-path", "legacy-project", "legacy-server", "/srv/managed/project", "observed", "main", "abc123"); err != nil {
				t.Fatal(err)
			}
		} else {
			worktree, err := database.Workspace(context.Background(), "legacy-worktree")
			if err != nil || worktree.ManagementMode != "managed" || worktree.Kind != "worktree" || worktree.ParentWorkspaceID == nil || *worktree.ParentWorkspaceID != "legacy-workspace" {
				t.Fatalf("historical PostgreSQL worktree ownership was not restored: %#v %v", worktree, err)
			}
			managedPath, err := database.Workspace(context.Background(), "legacy-managed-path")
			if err != nil || managedPath.ManagementMode != "managed" {
				t.Fatalf("historical PostgreSQL managed-root workspace was not restored: %#v %v", managedPath, err)
			}
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// The migration fixture intentionally has a reduced legacy servers table.
	// Exercise the scan lifecycle against a separate, complete production schema.
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE wio_scan_timeout`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	scanURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	scanURL.Path = "/wio_scan_timeout"
	scans, err := Open(scanURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer scans.Close()
	assertWorkspaceFileScanTimeoutAndRetry(t, scans)
	assertCodexSnapshotTimeoutAndRetry(t, scans)
	assertMessageForkCopiesOnlySelectedHistory(t, scans)
	assertGeminiCredentialBindingsAndReplay(t, scans)
	assertProjectServerPreferencesIsolation(t, scans)
	assertHiddenServerProjectsRecovery(t, scans)
	assertGeminiRecoveryOnPostgres(t, scans)
}

func assertGeminiRecoveryOnPostgres(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "recovery-node", []string{"/srv"}, "recovery-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "recovery-enrollment")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "recovery-node", "recovery-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/recovery-pg", Name: "recovery-pg"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var workspaceID string
	for _, workspace := range workspaces {
		if workspace.ServerID == server.ID && workspace.Path == "/srv/recovery-pg" {
			workspaceID = workspace.ID
		}
	}
	thread, err := database.CreateGeminiThread(ctx, workspaceID, "auto", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueGeminiTurn(ctx, thread, "work", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	thread, err = database.GeminiThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := database.ClaimGeminiDelivery(ctx, thread.OperationID); err != nil || !claimed {
		t.Fatalf("PostgreSQL delivery claim: %v %v", claimed, err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE agent_operations SET delivered_at=? WHERE id=?"), time.Now().UTC().Add(-time.Minute), thread.OperationID); err != nil {
		t.Fatal(err)
	}
	ids, err := database.UnfinishedGeminiOperations(ctx, server.ID)
	if err != nil || len(ids) != 1 || ids[0] != thread.OperationID {
		t.Fatalf("PostgreSQL reconciliation lookup: %v %v", ids, err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE gemini_threads SET status='interrupted' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, err = database.GeminiThread(ctx, thread.ID)
	if err != nil || !thread.RecoveryPending {
		t.Fatalf("PostgreSQL recovery status: %+v %v", thread, err)
	}
	if err := database.InterruptGeminiTurn(ctx, thread); err != nil {
		t.Fatalf("PostgreSQL retry interruption: %v", err)
	}
	operations, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(operations) != 1 || operations[0].Kind != "gemini.turn.interrupt" {
		t.Fatalf("PostgreSQL retry operation: %+v %v", operations, err)
	}
	var retry protocol.GeminiInterruptCommand
	if err := json.Unmarshal([]byte(operations[0].Payload), &retry); err != nil || !retry.RecoveryPending {
		t.Fatalf("PostgreSQL retry did not retain recovery state: %+v %v", retry, err)
	}
	// Simulate a failed verification before explicitly confirming process exit.
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE gemini_threads SET status='interrupted' WHERE id=?"), thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ConfirmGeminiStopped(ctx, thread.ID, thread.OperationID); err != nil {
		t.Fatal(err)
	}
	thread, err = database.GeminiThread(ctx, thread.ID)
	if err != nil || thread.RecoveryPending || thread.Status != "interrupted" {
		t.Fatalf("PostgreSQL confirmed recovery: %+v %v", thread, err)
	}
	// A concurrent retry and confirmation cannot both win and release an
	// operation while a new interruption request is being accepted.
	contended, err := database.CreateGeminiThread(ctx, workspaceID, "auto", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueGeminiTurn(ctx, contended, "work", "auto", "default"); err != nil {
		t.Fatal(err)
	}
	contended, err = database.GeminiThread(ctx, contended.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := database.ClaimGeminiDelivery(ctx, contended.OperationID); err != nil || !claimed {
		t.Fatalf("PostgreSQL contended delivery claim: %v %v", claimed, err)
	}
	if _, err := database.DB.ExecContext(ctx, database.Q("UPDATE gemini_threads SET status='interrupted' WHERE id=?"), contended.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- database.InterruptGeminiTurn(ctx, contended)
	}()
	go func() {
		<-start
		_, err := database.ConfirmGeminiStopped(ctx, contended.ID, contended.OperationID)
		results <- err
	}()
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) || (first != nil && !errors.Is(first, ErrThreadActive)) || (second != nil && !errors.Is(second, ErrThreadActive)) {
		t.Fatalf("PostgreSQL retry and confirmation outcomes: %v, %v", first, second)
	}
	current, err := database.GeminiThread(ctx, contended.ID)
	if err != nil {
		t.Fatal(err)
	}
	op, err := database.Operation(ctx, contended.OperationID)
	if err != nil || !((current.Status == "interrupting" && op.Status == "delivered") || (current.Status == "interrupted" && op.Status == "cancelled")) {
		t.Fatalf("PostgreSQL concurrent recovery inconsistent: thread=%+v operation=%+v error=%v", current, op, err)
	}
}

func availablePostgresPort(t *testing.T) uint32 {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return uint32(port)
}

func createLegacyPostgresSchema(t *testing.T, databaseURL string) {
	t.Helper()
	legacy, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	_, err = legacy.Exec(`
		CREATE TABLE servers (id TEXT PRIMARY KEY,name TEXT NOT NULL);
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,name TEXT NOT NULL,remote_url TEXT NOT NULL DEFAULT '',normalized_remote TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,path TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'moveing',
			branch TEXT NOT NULL DEFAULT '',commit_sha TEXT NOT NULL DEFAULT '',dirty INTEGER NOT NULL DEFAULT 0,
			last_scanned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(server_id,path)
		);
		CREATE TABLE agent_operations (
			id TEXT PRIMARY KEY,server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,kind TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',status TEXT NOT NULL DEFAULT 'queued',idempotency_key TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,delivered_at TIMESTAMP,completed_at TIMESTAMP,result TEXT
		);
		CREATE TABLE codex_threads (
			id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			codex_thread_id TEXT NOT NULL DEFAULT '',title TEXT NOT NULL DEFAULT 'New session',status TEXT NOT NULL DEFAULT 'idle',
			last_sequence INTEGER NOT NULL DEFAULT 0,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE credential_profiles (
			id TEXT PRIMARY KEY,kind TEXT NOT NULL,name TEXT NOT NULL,endpoint TEXT NOT NULL,username TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',ciphertext TEXT NOT NULL,key_version INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,UNIQUE(kind,name)
		);
		CREATE TABLE metric_rollups (
			server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,bucket_at TIMESTAMP NOT NULL,
			resolution TEXT NOT NULL DEFAULT 'minute',cpu_percent REAL NOT NULL,memory_percent REAL NOT NULL,disk_percent REAL NOT NULL,
			load_1 REAL NOT NULL,net_rx_bytes INTEGER NOT NULL,net_tx_bytes INTEGER NOT NULL,samples INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY(server_id,bucket_at,resolution)
		);
		INSERT INTO servers(id,name) VALUES('legacy-server','Legacy Server');
		INSERT INTO projects(id,name) VALUES('legacy-project','Legacy Project');
		INSERT INTO workspaces(id,project_id,server_id,path,branch,commit_sha) VALUES('legacy-workspace','legacy-project','legacy-server','/srv/legacy','main','abc123');
		INSERT INTO agent_operations(id,server_id,kind,idempotency_key,result) VALUES('legacy-operation','legacy-server','inventory.scan','legacy-scan','done');
		INSERT INTO codex_threads(id,workspace_id,title) VALUES('legacy-thread','legacy-workspace','Legacy thread');
		INSERT INTO credential_profiles(id,kind,name,endpoint,ciphertext) VALUES('legacy-git','git','Legacy Git','https://git.example.com','v1:test');
	`)
	if err != nil {
		t.Fatal(err)
	}
}

func assertPostgresMigrationAndStorage(t *testing.T, database *Store, exerciseStorage bool) {
	t.Helper()
	ctx := context.Background()
	if database.driver != "pgx" {
		t.Fatalf("unexpected database driver %q", database.driver)
	}
	project, err := database.Project(ctx, "legacy-project")
	if err != nil || project.Description != "" || project.DefaultBranch != "main" || project.Status != "ready" || project.ProvisionError != "" {
		t.Fatalf("unexpected migrated project: %#v %v", project, err)
	}
	workspace, err := database.Workspace(ctx, "legacy-workspace")
	if err != nil || workspace.DisplayName != "" || workspace.ManagementMode != "observed" || workspace.Status != "moving" || workspace.Kind != "primary" || workspace.GitError != "" {
		t.Fatalf("unexpected migrated workspace: %#v %v", workspace, err)
	}
	operation, err := database.Operation(ctx, "legacy-operation")
	if err != nil || operation.ProjectID != "" || operation.WorkspaceID != "" || operation.WorkspaceWrite != 0 || operation.ResultData != "{}" {
		t.Fatalf("unexpected migrated operation: %#v %v", operation, err)
	}
	for table, expected := range map[string][]string{
		"projects":            {"description", "default_branch", "status", "provision_error", "archived_at"},
		"workspaces":          {"display_name", "management_mode", "status", "kind", "parent_workspace_id", "last_git_refresh_at", "git_error"},
		"agent_operations":    {"project_id", "workspace_id", "workspace_write", "result_data"},
		"servers":             {"managed_roots"},
		"credential_profiles": {"commit_name", "commit_email"},
		"codex_threads":       {"pinned_at", "archived_at"},
		"metric_rollups":      {"memory_used_bytes", "memory_total_bytes", "disk_used_bytes", "disk_total_bytes"},
	} {
		var columns []string
		if err := database.DB.SelectContext(ctx, &columns, "SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1", table); err != nil {
			t.Fatal(err)
		}
		for _, column := range expected {
			if !containsString(columns, column) {
				t.Fatalf("%s migration missing %s: %v", table, column, columns)
			}
		}
	}
	profile, err := database.CredentialProfile(ctx, "legacy-git")
	if err != nil || profile.CommitName != "" || profile.CommitEmail != "" {
		t.Fatalf("unexpected migrated credential profile: %#v %v", profile, err)
	}
	var metricColumnType string
	if err := database.DB.GetContext(ctx, &metricColumnType, "SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='metric_rollups' AND column_name='net_rx_bytes'"); err != nil || metricColumnType != "bigint" {
		t.Fatalf("metric counter migration did not produce BIGINT: %q %v", metricColumnType, err)
	}
	if !exerciseStorage {
		return
	}

	operationID, err := database.QueueResourceOperation(ctx, "legacy-server", "git.workspace.write", map[string]string{"branch": "feature/postgres"}, "postgres-write", OperationResource{ProjectID: project.ID, WorkspaceID: workspace.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.QueueResourceOperation(ctx, "legacy-server", "git.workspace.write", map[string]string{"branch": "other"}, "postgres-write-conflict", OperationResource{ProjectID: project.ID, WorkspaceID: workspace.ID}, true); !errors.Is(err, ErrWorkspaceWriteActive) {
		t.Fatalf("concurrent PostgreSQL workspace write was not rejected: %v", err)
	}
	resultData, _ := json.Marshal(map[string]string{"branch": "feature/postgres"})
	if err := database.CompleteOperation(ctx, protocol.OperationResult{OperationID: operationID, Status: "succeeded", Message: "created", Data: resultData}); err != nil {
		t.Fatal(err)
	}
	operation, err = database.Operation(ctx, operationID)
	if err != nil || operation.Status != "succeeded" || operation.ResultData != string(resultData) {
		t.Fatalf("unexpected PostgreSQL operation result: %#v %v", operation, err)
	}

	const networkBytes = uint64(1 << 40)
	assertMetricCapacityStorage(t, database, "legacy-server")
	if err := database.SaveMetrics(ctx, "legacy-server", protocol.Metrics{CPUPercent: 1, MemoryPercent: 2, DiskPercent: 3, Load1: 4, NetRxBytes: networkBytes, NetTxBytes: networkBytes + 1}); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Rx int64 `db:"net_rx_bytes"`
		Tx int64 `db:"net_tx_bytes"`
	}
	if err := database.DB.GetContext(ctx, &stored, "SELECT net_rx_bytes,net_tx_bytes FROM metric_rollups WHERE server_id=$1", "legacy-server"); err != nil || stored.Rx != int64(networkBytes) || stored.Tx != int64(networkBytes+1) {
		t.Fatalf("unexpected PostgreSQL BIGINT metrics: %#v %v", stored, err)
	}
}
