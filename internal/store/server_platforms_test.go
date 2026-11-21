package store

import (
	"context"
	"github.com/wio-platform/wio/internal/protocol"
	"testing"
)

func TestHeartbeatPlatformAndLegacyPreservation(t *testing.T) {
	assertHeartbeatPlatform(t, testStore(t))
}
func assertHeartbeatPlatform(t *testing.T, db *Store) {
	ctx := context.Background()
	server := createOperationTestServer(t, db, "windows", "windows-token")
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "windows", OS: "windows", Architecture: "amd64"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Server(ctx, server.ID)
	if err != nil || got.OS != "windows" || got.Architecture != "amd64" || got.DeploymentSupported {
		t.Fatalf("platform=%+v %v", got, err)
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "legacy"}); err != nil {
		t.Fatal(err)
	}
	got, err = db.Server(ctx, server.ID)
	if err != nil || got.OS != "windows" {
		t.Fatal("legacy heartbeat discarded known platform")
	}
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{Hostname: "bad", OS: "windows", Architecture: "arm64"}); err == nil {
		t.Fatal("unsupported platform persisted")
	}
	got, _ = db.Server(ctx, server.ID)
	if got.Hostname != "legacy" {
		t.Fatal("invalid platform partially committed heartbeat")
	}
	if err := db.RevokeServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.DB.GetContext(ctx, &remaining, db.Q("SELECT COUNT(*) FROM server_platforms WHERE server_id=?"), server.ID); err != nil || remaining != 0 {
		t.Fatalf("platform survived retirement: %d %v", remaining, err)
	}
}
func TestRepairRegistrationKeepsServerIdentityAndData(t *testing.T) {
	assertRepairRegistration(t, testStore(t))
}
func assertRepairRegistration(t *testing.T, db *Store) {
	ctx := context.Background()
	server := createOperationTestServer(t, db, "repair", "old-token")
	registration, err := db.CreateServerRegistration(ctx, "repair", "code", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.BindRepairRegistration(ctx, registration.ID, server.ID); err != nil {
		t.Fatal(err)
	}
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.ClaimServerRegistration(ctx, registration.ID, "code", key); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		id, err := db.EnrollServerRegistration(ctx, registration.ID, key, "repair-host", "new-token", Enrollment{ServerName: "ignored", ScanRoots: `["/should-not-overwrite"]`})
		if err != nil || id != server.ID {
			t.Fatalf("repair=%s %v", id, err)
		}
	}
	got, err := db.Server(ctx, server.ID)
	if err != nil || got.Name != server.Name || got.ScanRoots != server.ScanRoots {
		t.Fatal("repair changed existing metadata")
	}
	if _, err := db.AuthenticateAgent(ctx, "old-token"); err == nil {
		t.Fatal("old credential remained valid")
	}
	if id, err := db.AuthenticateAgent(ctx, "new-token"); err != nil || id != server.ID {
		t.Fatal("new credential rejected")
	}
}
func TestWindowsManagedPathBoundaries(t *testing.T) {
	if !insideManagedWorkspaceRoot(`c:\Projects\repo`, []string{`C:/Projects`}) {
		t.Fatal("Windows managed root rejected")
	}
	if insideManagedWorkspaceRoot(`C:\Projects-evil\repo`, []string{`C:\Projects`}) {
		t.Fatal("sibling escaped managed root")
	}
	if !containsArtifactPath(`C:\Projects\repo`, `c:/projects/repo/subdir`) {
		t.Fatal("Windows overlapping artifact root missed")
	}
}
