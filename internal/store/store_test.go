package store

import (
	"context"
	"database/sql"
	"path/filepath"
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
	operationID, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]bool{"now": true}, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	duplicateID, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]bool{"now": true}, "scan-1")
	if err != nil || duplicateID != operationID {
		t.Fatalf("idempotency failed: %s %s %v", operationID, duplicateID, err)
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
}
