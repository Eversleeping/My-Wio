package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerRegistrationLifecycle(t *testing.T) {
	assertServerRegistrationLifecycle(t, testStore(t))
}
func assertServerRegistrationLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	key := strings.Repeat("a", 64)
	r, err := s.CreateServerRegistration(ctx, "command-node", "one-time-code", `{"name":"command-node"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimServerRegistration(ctx, r.ID, "wrong", key); err == nil {
		t.Fatal("wrong code was accepted")
	}
	claimed, err := s.ClaimServerRegistration(ctx, r.ID, "one-time-code", key)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.CodeHash == "one-time-code" || claimed.DeviceHash == key || claimed.State != "installing" {
		t.Fatal("ownership was not hashed and committed")
	}
	raw, _ := json.Marshal(claimed)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), claimed.CodeHash) || strings.Contains(string(raw), "config_json") {
		t.Fatal("public registration exposes private data")
	}
	if _, err := s.ClaimServerRegistration(ctx, r.ID, "one-time-code", strings.Repeat("b", 64)); err == nil {
		t.Fatal("another device replayed the code")
	}
	if _, err := s.DB.ExecContext(ctx, s.Q(`UPDATE server_registrations SET expires_at=? WHERE id=?`), time.Now().UTC().Add(-time.Minute), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimServerRegistration(ctx, r.ID, "", key); err != nil {
		t.Fatalf("claimed device cannot resume past original expiry: %v", err)
	}
	e := Enrollment{ServerName: "command-node", ScanRoots: `["/srv"]`, Notes: "metadata"}
	id, err := s.EnrollServerRegistration(ctx, r.ID, key, "node.local", "stable-agent-token", e)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnrollServerRegistration(ctx, r.ID, key, "node.local", "stable-agent-token", e)
	if err != nil || again != id {
		t.Fatalf("retry duplicated identity: %q %q %v", id, again, err)
	}
	if _, err := s.EnrollServerRegistration(ctx, r.ID, key, "node.local", "changed-token", e); err == nil {
		t.Fatal("retry changed Agent identity")
	}
	if _, err := s.AuthenticateAgent(ctx, "stable-agent-token"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateServerRegistrationProgress(ctx, r.ID, key, "waiting_online", `[{"step":"starting_service"}]`, `{}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteServerRegistration(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = s.ServerRegistration(ctx, r.ID)
	if err != nil || r.State != "complete" {
		t.Fatalf("completion lost: %+v %v", r, err)
	}
	if err := s.RevokeServerRegistration(ctx, r.ID); err == nil {
		t.Fatal("completed registration was revoked")
	}
	revoked, err := s.CreateServerRegistration(ctx, "revoked-node", "second-code", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimServerRegistration(ctx, revoked.ID, "second-code", key); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeServerRegistration(ctx, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateServerRegistration(ctx, revoked.ID, key); err == nil {
		t.Fatal("revoked device authenticated")
	}
	if err := s.UpdateServerRegistrationProgress(ctx, revoked.ID, key, "installing", "[]", "{}", ""); err == nil {
		t.Fatal("revoked device wrote progress")
	}
	expired, err := s.CreateServerRegistration(ctx, "expired-node", "third-code", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, s.Q(`UPDATE server_registrations SET expires_at=? WHERE id=?`), time.Now().UTC().Add(-time.Minute), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimServerRegistration(ctx, expired.ID, "third-code", key); err == nil {
		t.Fatal("expired code was accepted")
	}
}
func TestServerRegistrationClaimRace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r, err := s.CreateServerRegistration(ctx, "race", "code", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, key := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			if _, err := s.ClaimServerRegistration(ctx, r.ID, "code", key); err == nil {
				results <- key
			}
		}(key)
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("expected one owner, got %d", len(results))
	}
}
