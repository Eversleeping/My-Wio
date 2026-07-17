package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

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
