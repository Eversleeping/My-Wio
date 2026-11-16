package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
)

func TestServerAPIExposesMinimumWithoutBlockingLegacyRecovery(t *testing.T) {
	db := openBootstrapTestStore(t)
	server := enrollResourceTestServer(t, db, "server-baseline")
	api := resourceTestAPI(db)
	for _, tc := range []struct {
		version             string
		upgrade, selfUpdate bool
	}{
		{"", true, false}, {"invalid", true, false}, {"0.1.9", true, false},
		{"0.2.0", true, true}, {"0.3.9", true, true}, {"0.3.25", true, true},
		{"0.3.26", false, true}, {"0.3.100", false, true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if err := db.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{AgentVersion: tc.version, Capabilities: testCapabilities("0.66.0", "2.1.286")}); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			api.servers(response, httptest.NewRequest("GET", "/api/servers", nil))
			var rows []store.Server
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rows) != nil || len(rows) != 1 {
				t.Fatalf("list: %d %s", response.Code, response.Body)
			}
			detail, err := db.Server(context.Background(), server.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range []store.Server{rows[0], detail} {
				if row.AgentUpgradeRequired != tc.upgrade || row.AgentMinimumVersion != "0.3.26" {
					t.Fatalf("baseline: %+v", row)
				}
			}
			if rows[0].AgentUpdateSupported != tc.selfUpdate {
				t.Fatalf("recovery: %+v", rows[0])
			}
			if tc.upgrade && (rows[0].CLIManagementSupported || rows[0].ScopedCredentialsSupported) {
				t.Fatal("old Agent granted operations")
			}
		})
	}
}
