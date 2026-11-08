package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func createReplayTestUser(t *testing.T, database *Store, recovery ...string) User {
	t.Helper()
	raw, err := json.Marshal(append([]string{}, recovery...))
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: NewID(), Username: "admin-" + NewID(), AuthMode: AuthModeTOTP, PasswordHash: "unused", TOTPSecret: "unused", RecoveryHashes: string(raw)}
	if err := database.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return user
}

func storedRecoveryHashes(t *testing.T, database *Store, userID string) []string {
	t.Helper()
	var raw string
	if err := database.DB.GetContext(context.Background(), &raw, database.Q("SELECT recovery_hashes FROM users WHERE id=?"), userID); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
		t.Fatalf("stored recovery hashes %q: %v", raw, err)
	}
	return hashes
}

func assertTOTPCounterMonotonic(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	user := createReplayTestUser(t, database)
	for _, step := range []struct {
		counter int64
		want    bool
	}{{100, true}, {100, false}, {99, false}, {101, true}, {100, false}} {
		got, err := database.AdvanceTOTPCounter(ctx, user.ID, step.counter)
		if err != nil || got != step.want {
			t.Fatalf("advance to %d = %v, %v; want %v", step.counter, got, err, step.want)
		}
	}
	if got, err := database.AdvanceTOTPCounter(ctx, "unknown-user", 1); err != nil || got {
		t.Fatalf("unknown user advanced: %v %v", got, err)
	}
}

func assertRecoveryCodesSingleUse(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	user := createReplayTestUser(t, database, "first", "second", "third")
	for _, step := range []struct {
		hash string
		want bool
	}{{"first", true}, {"first", false}, {"missing", false}, {"second", true}} {
		got, err := database.ConsumeRecoveryCode(ctx, user.ID, step.hash)
		if err != nil || got != step.want {
			t.Fatalf("consume %q = %v, %v; want %v", step.hash, got, err, step.want)
		}
	}
	if remaining := storedRecoveryHashes(t, database, user.ID); len(remaining) != 1 || remaining[0] != "third" {
		t.Fatalf("unexpected remaining codes: %v", remaining)
	}
	if got, err := database.ConsumeRecoveryCode(ctx, user.ID, "third"); err != nil || !got {
		t.Fatalf("consume last code: %v %v", got, err)
	}
	if remaining := storedRecoveryHashes(t, database, user.ID); len(remaining) != 0 {
		t.Fatalf("all codes spent but %v remain", remaining)
	}
	if got, err := database.ConsumeRecoveryCode(ctx, "unknown-user", "first"); err != nil || got {
		t.Fatalf("unknown user consumed a code: %v %v", got, err)
	}
}

// parallelCalls runs call concurrently and reports how many returned true.
func parallelCalls(t *testing.T, count int, call func(index int) (bool, error)) int {
	t.Helper()
	var group sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	successes := 0
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			ok, err := call(index)
			if err != nil {
				t.Errorf("call %d failed: %v", index, err)
				return
			}
			if ok {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(index)
	}
	close(start)
	group.Wait()
	return successes
}

func assertConcurrentSingleUse(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	const attempts = 10
	// Separate rounds warm the connection pool and repeat the race, because a
	// single round can finish before the consumers actually overlap.
	for round := 0; round < 30; round++ {
		token := "race-enrollment-" + NewID()
		if _, err := database.CreateEnrollment(ctx, "race-node", []string{"/srv"}, token, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		consumed := parallelCalls(t, attempts, func(int) (bool, error) {
			_, err := database.ConsumeEnrollment(ctx, token)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return err == nil, err
		})
		if consumed != 1 {
			t.Fatalf("round %d: %d of %d concurrent enrollment consumes succeeded", round, consumed, attempts)
		}
	}

	user := createReplayTestUser(t, database)
	const counter = int64(55_000_000)
	if advanced := parallelCalls(t, attempts, func(int) (bool, error) {
		return database.AdvanceTOTPCounter(ctx, user.ID, counter)
	}); advanced != 1 {
		t.Fatalf("%d of %d concurrent TOTP uses succeeded", advanced, attempts)
	}

	shared := createReplayTestUser(t, database, "shared", "other")
	if spent := parallelCalls(t, attempts, func(int) (bool, error) {
		return database.ConsumeRecoveryCode(ctx, shared.ID, "shared")
	}); spent != 1 {
		t.Fatalf("%d of %d concurrent uses of one recovery code succeeded", spent, attempts)
	}
	if remaining := storedRecoveryHashes(t, database, shared.ID); len(remaining) != 1 || remaining[0] != "other" {
		t.Fatalf("unexpected codes after contention: %v", remaining)
	}

	// Distinct codes must all succeed and none may be restored by a stale write.
	codes := make([]string, attempts)
	for index := range codes {
		codes[index] = "code-" + NewID()
	}
	distinct := createReplayTestUser(t, database, codes...)
	if spent := parallelCalls(t, attempts, func(index int) (bool, error) {
		return database.ConsumeRecoveryCode(ctx, distinct.ID, codes[index])
	}); spent != attempts {
		t.Fatalf("%d of %d concurrent distinct recovery codes succeeded", spent, attempts)
	}
	if remaining := storedRecoveryHashes(t, database, distinct.ID); len(remaining) != 0 {
		t.Fatalf("spent recovery codes were resurrected: %v", remaining)
	}
}

func assertProjectNameFilterSemantics(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	for index, name := range []string{"Alpha_Beta", "alpha-beta", "100% Done", "100 Done", "Back\\slash", "Plain Project"} {
		if _, err := database.CreateProject(ctx, name, "https://example.com/filter/"+string(rune('a'+index))+".git"); err != nil {
			t.Fatal(err)
		}
	}
	for filter, want := range map[string][]string{
		"ALPHA_":    {"Alpha_Beta"},
		"alpha":     {"Alpha_Beta", "alpha-beta"},
		"%":         {"100% Done"},
		"_":         {"Alpha_Beta"},
		`K\S`:       {"Back\\slash"},
		"plain pro": {"Plain Project"},
		"absent":    {},
	} {
		projects, err := database.ListProjectsFiltered(ctx, filter, "", "")
		if err != nil {
			t.Fatalf("filter %q: %v", filter, err)
		}
		got := make(map[string]bool, len(projects))
		for _, project := range projects {
			got[project.Name] = true
		}
		if len(got) != len(want) {
			t.Fatalf("filter %q matched %v, want %v", filter, got, want)
		}
		for _, name := range want {
			if !got[name] {
				t.Fatalf("filter %q matched %v, want %v", filter, got, want)
			}
		}
	}
}

func TestAdvanceTOTPCounterOnlyMovesForward(t *testing.T) {
	assertTOTPCounterMonotonic(t, testStore(t))
}

func TestConsumeRecoveryCodeIsSingleUse(t *testing.T) {
	assertRecoveryCodesSingleUse(t, testStore(t))
}

func TestConcurrentCredentialConsumptionIsSingleUse(t *testing.T) {
	assertConcurrentSingleUse(t, testStore(t))
}

func TestListProjectsFilteredIsCaseInsensitiveAndLiteral(t *testing.T) {
	assertProjectNameFilterSemantics(t, testStore(t))
}

func TestOpenAddsTOTPCounterToLegacyUsersIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-totp.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY,username TEXT NOT NULL UNIQUE,auth_mode TEXT NOT NULL DEFAULT 'totp',password_hash TEXT NOT NULL,totp_secret TEXT NOT NULL,recovery_hashes TEXT NOT NULL,created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes) VALUES('legacy-user','admin','unused','encrypted','[]')`)
	if closeErr := legacy.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		database, err := Open(path + "?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt, err)
		}
		if err := migrateUserTOTPCounter(context.Background(), database.DB, database.driver); err != nil {
			t.Fatalf("repeat migration attempt %d: %v", attempt, err)
		}
		var counter int64
		if err := database.DB.Get(&counter, "SELECT totp_last_counter FROM users WHERE id='legacy-user'"); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 && counter != 0 {
			t.Fatalf("legacy user counter = %d", counter)
		}
		if advanced, err := database.AdvanceTOTPCounter(context.Background(), "legacy-user", int64(1000*attempt)); err != nil || !advanced {
			t.Fatalf("advance after migration attempt %d: %v %v", attempt, advanced, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
