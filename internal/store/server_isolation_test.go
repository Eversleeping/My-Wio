package store

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestHeartbeatSessionIsolation(t *testing.T) {
	assertHeartbeatSessionIsolation(t, testStore(t))
}

func assertHeartbeatSessionIsolation(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	server := createOperationTestServer(t, db, "session-isolation", "session-isolation-token")
	heartbeat := protocol.Heartbeat{AgentVersion: "0.3.31", CodexVersion: "synthetic-old", Capabilities: []string{protocol.CapabilitySessionIsolation}}
	check := func(want *protocol.SessionIsolationStatus) {
		t.Helper()
		row, err := db.Server(ctx, server.ID)
		if err != nil || !reflect.DeepEqual(row.SessionIsolation, want) {
			t.Fatalf("server isolation state differs: got=%v want=%v err=%v", row.SessionIsolation, want, err)
		}
		rows, err := db.ListServers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == server.ID {
				if !reflect.DeepEqual(row.SessionIsolation, want) {
					t.Fatal("server list and detail disagree about isolation")
				}
				return
			}
		}
		t.Fatal("missing server isolation row")
	}
	check(nil)
	for _, status := range []protocol.SessionIsolationStatus{{Mode: protocol.IsolationEnabled}, {Mode: protocol.IsolationUnsupported, Reason: "Required kernel support is unavailable"}, {Mode: protocol.IsolationDisabled}} {
		heartbeat.SessionIsolation = &status
		if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
			t.Fatal(err)
		}
		check(&status)
	}
	heartbeat.SessionIsolation = &protocol.SessionIsolationStatus{Mode: protocol.IsolationEnabled, Reason: "stale diagnostic"}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	check(&protocol.SessionIsolationStatus{Mode: protocol.IsolationEnabled})
	// Inject a failure in the final statement: no earlier heartbeat update may
	// commit and make an unavailable boundary appear healthy.
	if db.isPostgres() {
		for _, sql := range []string{`CREATE FUNCTION fail_isolation_heartbeat() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected isolation failure'; END $$`, `CREATE TRIGGER fail_isolation_heartbeat BEFORE INSERT OR UPDATE ON server_session_isolation FOR EACH ROW EXECUTE FUNCTION fail_isolation_heartbeat()`} {
			if _, err := db.DB.Exec(sql); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		if _, err := db.DB.Exec(`CREATE TRIGGER fail_isolation_heartbeat BEFORE UPDATE ON server_session_isolation BEGIN SELECT RAISE(ABORT,'injected isolation failure'); END`); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat.CodexVersion = "uncommitted"
	heartbeat.SessionIsolation = &protocol.SessionIsolationStatus{Mode: protocol.IsolationUnsupported, Reason: "uncommitted"}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err == nil || !strings.Contains(err.Error(), "injected isolation failure") {
		t.Fatal("isolation heartbeat failure was not propagated")
	}
	check(&protocol.SessionIsolationStatus{Mode: protocol.IsolationEnabled})
	row, err := db.Server(ctx, server.ID)
	if err != nil || row.CodexVersion != "synthetic-old" {
		t.Fatal("isolation failure partially committed the heartbeat")
	}
	if db.isPostgres() {
		for _, sql := range []string{`DROP TRIGGER fail_isolation_heartbeat ON server_session_isolation`, `DROP FUNCTION fail_isolation_heartbeat()`} {
			if _, err := db.DB.Exec(sql); err != nil {
				t.Fatal(err)
			}
		}
	} else if _, err := db.DB.Exec(`DROP TRIGGER fail_isolation_heartbeat`); err != nil {
		t.Fatal(err)
	}
	heartbeat.SessionIsolation = nil // An older Agent must clear a stale claim.
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	check(nil)
	heartbeat.SessionIsolation = &protocol.SessionIsolationStatus{Mode: "future-state"}
	if err := db.Heartbeat(ctx, server.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	row, err = db.Server(ctx, server.ID)
	if err != nil || row.SessionIsolation == nil || row.SessionIsolation.Mode != protocol.IsolationUnsupported || row.SessionIsolation.Reason == "" {
		t.Fatal("unknown isolation state was treated as enabled")
	}
}
