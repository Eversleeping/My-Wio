package store

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
)

func TestHeartbeatCapabilities(t *testing.T) { assertHeartbeatCapabilities(t, testStore(t)) }

// Also exercised on PostgreSQL, including a failure in the last statement of
// the heartbeat. Server, CLI and declaration must all roll back together.
func assertHeartbeatCapabilities(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "capabilities", "capabilities-token")
	op, err := db.QueueOperation(ctx, server.ID, protocol.OpWorkspaceFiles, protocol.WorkspaceFilesCommand{}, "capability-status")
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := protocol.Heartbeat{AgentVersion: "9.9.9", CodexVersion: "old", GeminiVersion: "0.66.0", ClaudeVersion: "2.1.286", Capabilities: []string{protocol.CapabilityAgentSelfUpdate, "future.unknown"}}
	check := func(want capabilities.Declaration, interrupt bool) {
		t.Helper()
		row, err := db.Server(ctx, server.ID)
		if err != nil || !reflect.DeepEqual(row.DeclaredCapabilities, want) {
			t.Fatalf("server declaration=%v want=%v: %v", row.DeclaredCapabilities, want, err)
		}

		tx, err := db.DB.BeginTxx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		supported, err := (transactionPort{store: db, tx: tx}).SupportsCollaboration(ctx, server.ID)
		_ = tx.Rollback()
		if err != nil || supported != row.Supports(protocol.CapabilitySessionCollaboration) {
			t.Fatalf("transactional collaboration support=%v: %v", supported, err)
		}
		list, err := db.ListServers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range list {
			if row.ID == server.ID {
				found = true
				if !reflect.DeepEqual(row.DeclaredCapabilities, want) {
					t.Fatal("list declaration differs")
				}
			}
		}
		if !found {
			t.Fatal("missing server")
		}
		ops, err := db.ListOperations(ctx, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, row := range ops {
			if row.ID == op {
				found = true
				if row.InterruptSupported != interrupt {
					t.Fatalf("interrupt=%v want %v", row.InterruptSupported, interrupt)
				}
			}
		}
		if !found {
			t.Fatal("missing operation")
		}
	}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	check(capabilities.Declaration(heartbeat.Capabilities), false)
	// Unknown names must not make the declaration appear absent or enable gates.
	row, err := db.Server(ctx, server.ID)
	if err != nil || !row.Supports(protocol.CapabilityAgentSelfUpdate) || row.Supports(protocol.CapabilityCodexSteer) || row.Supports("future.unknown") {
		t.Fatalf("authoritative set ignored: %+v %v", row, err)
	}
	// Fail only the declaration write, after the server and three CLI updates.
	if db.isPostgres() {
		for _, stmt := range []string{`CREATE FUNCTION fail_capability_heartbeat() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected declaration failure'; END $$`, `CREATE TRIGGER fail_capability_heartbeat BEFORE INSERT OR UPDATE ON server_capabilities FOR EACH ROW EXECUTE FUNCTION fail_capability_heartbeat()`} {
			if _, err := db.DB.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		if _, err := db.DB.Exec(`CREATE TRIGGER fail_capability_heartbeat BEFORE UPDATE ON server_capabilities BEGIN SELECT RAISE(ABORT,'injected declaration failure'); END`); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat.AgentVersion = "0.1.0"
	heartbeat.CodexVersion = "uncommitted"
	heartbeat.Capabilities = []string{protocol.CapabilityOperationInterrupt}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err == nil || !strings.Contains(err.Error(), "injected declaration failure") {
		t.Fatalf("missing failure: %v", err)
	}
	row, err = db.Server(ctx, server.ID)
	if err != nil || row.AgentVersion != "9.9.9" || row.CodexVersion != "old" || !row.Supports(protocol.CapabilityAgentSelfUpdate) {
		t.Fatalf("partial heartbeat committed: %+v %v", row, err)
	}
	if db.isPostgres() {
		if _, err := db.DB.Exec(`DROP TRIGGER fail_capability_heartbeat ON server_capabilities`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.Exec(`DROP FUNCTION fail_capability_heartbeat()`); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := db.DB.Exec(`DROP TRIGGER fail_capability_heartbeat`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	check(capabilities.Declaration(heartbeat.Capabilities), true) // low version overridden
	heartbeat.AgentVersion = "9.9.9"
	heartbeat.Capabilities = []string{}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	check(capabilities.Declaration{}, false)
	// A downgrade to a legacy heartbeat clears the row and restores old inference.
	heartbeat.Capabilities = nil
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	check(nil, true)
	var count int
	if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM server_capabilities WHERE server_id=?"), server.ID); err != nil || count != 0 {
		t.Fatalf("stale row=%d %v", count, err)
	}
	heartbeat.Capabilities = []string{protocol.CapabilityAgentSelfUpdate}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.Get(&count, db.Q("SELECT COUNT(*) FROM server_capabilities WHERE server_id=?"), server.ID); err != nil || count != 0 {
		t.Fatalf("revoked declaration remains: %d %v", count, err)
	}
}
