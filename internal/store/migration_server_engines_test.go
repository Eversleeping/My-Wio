package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

// This independent legacy projection is also the oracle for retained-copy
// migration checks. It includes absent bindings and revoked server history.
const legacyServerEngineRows = `SELECT s.id AS server_id,'codex' AS engine,s.codex_version AS cli_version,s.codex_ready AS ready,cp.codex_profile_id AS credential_profile_id,'' AS profile_revision,'' AS model,cp.updated_at
 FROM servers s LEFT JOIN server_credential_profiles cp ON cp.server_id=s.id
 UNION ALL SELECT s.id,'gemini',s.gemini_version,s.gemini_ready,cp.profile_id,COALESCE(cp.profile_revision,''),'',cp.updated_at
 FROM servers s LEFT JOIN server_gemini_credential_profiles cp ON cp.server_id=s.id
 UNION ALL SELECT s.id,'claude',COALESCE(cli.version,''),COALESCE(cli.ready,0),cp.profile_id,COALESCE(cp.profile_revision,''),COALESCE(cp.model,''),cp.updated_at
 FROM servers s LEFT JOIN server_claude_cli cli ON cli.server_id=s.id LEFT JOIN server_claude_credential_profiles cp ON cp.server_id=s.id`

func TestServerEngineMigration(t *testing.T) {
	db, err := sqlx.Connect("sqlite", filepath.Join(t.TempDir(), "engines.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	assertServerEngineMigration(t, db, "sqlite")
}

func assertServerEngineMigration(t *testing.T, db *sqlx.DB, driver string) {
	t.Helper()
	ctx := context.Background()
	if _, err := runMigrations(ctx, db, driver, schemaMigrations[:2]); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db, driver: driver}
	at := time.Date(2026, 11, 15, 1, 2, 3, 123456000, time.UTC)
	for _, stmt := range []string{
		`INSERT INTO servers(id,name,codex_version,codex_ready,gemini_version,gemini_ready) VALUES('bound','Bound','codex-old',1,'gemini-old',1)`,
		`INSERT INTO servers(id,name) VALUES('empty','Empty'),('unbound','Unbound'),('revoked','Revoked')`,
		`UPDATE servers SET revoked_at=CURRENT_TIMESTAMP WHERE id='revoked'`,
		`INSERT INTO credential_profiles(id,kind,name,endpoint,ciphertext,model) VALUES('codex','codex','Codex','','v1:fixture',''),('git','git','Git','','v1:fixture',''),('gemini','gemini','Gemini','','v1:fixture',''),('claude','claude','Claude','','v1:fixture','edited-default')`,
		`INSERT INTO server_claude_cli(server_id,version,ready) VALUES('bound','claude-old',1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`INSERT INTO server_credential_profiles(server_id,codex_profile_id,git_profile_id,updated_at) VALUES('bound','codex','git',?)`,
		`INSERT INTO server_credential_profiles(server_id,updated_at) VALUES('empty',?)`,
		`INSERT INTO server_gemini_credential_profiles(server_id,profile_id,profile_revision,updated_at) VALUES('bound','gemini','applied-gemini-revision',?)`,
		`INSERT INTO server_gemini_credential_profiles(server_id,updated_at) VALUES('empty',?)`,
		`INSERT INTO server_claude_credential_profiles(server_id,profile_id,profile_revision,model,updated_at) VALUES('bound','claude','applied-claude-revision','applied-snapshot',?)`,
		`INSERT INTO server_claude_credential_profiles(server_id,updated_at) VALUES('empty',?)`,
	} {
		if _, err := db.Exec(s.Q(stmt), at); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := runMigrations(ctx, db, driver, schemaMigrations); err != nil {
			t.Fatal(err)
		}
	}
	assertServerEngineCopies(t, db)
	var count int
	if err := db.Get(&count, "SELECT COUNT(*) FROM server_engines"); err != nil || count != 12 {
		t.Fatalf("engine count=%d: %v", count, err)
	}
	if err := db.Get(&count, "SELECT COUNT(*) FROM server_engines WHERE server_id='unbound' AND updated_at IS NULL"); err != nil || count != 3 {
		t.Fatalf("absent binding count=%d: %v", count, err)
	}
	bound, err := s.Server(ctx, "bound")
	if err != nil || bound.CodexVersion != "codex-old" || bound.ClaudeVersion != "claude-old" || bound.GeminiVersion != "gemini-old" || bound.CodexProfileID != "codex" || bound.GitProfileID != "git" || bound.ClaudeProfileModel != "applied-snapshot" {
		t.Fatalf("server projection changed: %+v %v", bound, err)
	}
	// A heartbeat changes only CLI fields; old sources remain frozen.
	if err := s.Heartbeat(ctx, "bound", protocol.Heartbeat{CodexVersion: "codex-new", GeminiVersion: "gemini-new", ClaudeVersion: "claude-new"}); err != nil {
		t.Fatal(err)
	}
	var snapshot string
	if err := db.Get(&snapshot, "SELECT model FROM server_claude_credential_profiles WHERE server_id='bound'"); err != nil || snapshot != "applied-snapshot" {
		t.Fatalf("legacy binding changed: %q %v", snapshot, err)
	}
	if err := db.Get(&snapshot, "SELECT codex_version FROM servers WHERE id='bound'"); err != nil || snapshot != "codex-old" {
		t.Fatalf("legacy CLI column changed: %q %v", snapshot, err)
	}
	if err := db.Get(&snapshot, "SELECT version FROM server_claude_cli WHERE server_id='bound'"); err != nil || snapshot != "claude-old" {
		t.Fatalf("legacy Claude CLI changed: %q %v", snapshot, err)
	}
	// Git-only updates preserve the new Codex binding and frozen old Codex column.
	if err := s.inTx(ctx, nil, func(tx *sqlx.Tx) error { return s.setServerCredentialProfileIDs(ctx, tx, "bound", "", nil, "git") }); err != nil {
		t.Fatal(err)
	}
	bound, err = s.Server(ctx, "bound")
	if err != nil || bound.CodexProfileID != "codex" || bound.GitProfileID != "" {
		t.Fatalf("Git scope overwrote Codex: %+v %v", bound, err)
	}
	if err := s.inTx(ctx, nil, func(tx *sqlx.Tx) error { return s.setServerCredentialProfileIDs(ctx, tx, "bound", "", nil, "codex") }); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&snapshot, "SELECT codex_profile_id FROM server_credential_profiles WHERE server_id='bound'"); err != nil || snapshot != "codex" {
		t.Fatalf("legacy Codex binding changed: %q %v", snapshot, err)
	}
	if err := s.DeleteCredentialProfile(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&count, "SELECT COUNT(*) FROM server_engines WHERE server_id='bound' AND engine='claude' AND credential_profile_id IS NULL AND model='applied-snapshot'"); err != nil || count != 1 {
		t.Fatalf("credential delete lost binding metadata: %d %v", count, err)
	}
	if err := s.RevokeServer(ctx, "bound"); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&count, "SELECT COUNT(*) FROM server_engines WHERE server_id='bound'"); err != nil || count != 0 {
		t.Fatalf("server delete left engine rows: %d %v", count, err)
	}
}

func assertServerEngineCopies(t *testing.T, db *sqlx.DB) {
	t.Helper()
	for _, query := range []string{
		"SELECT COUNT(*) FROM (" + legacyServerEngineRows + " EXCEPT SELECT * FROM server_engines) differences",
		"SELECT COUNT(*) FROM (SELECT * FROM server_engines EXCEPT SELECT * FROM (" + legacyServerEngineRows + ") legacy) differences",
	} {
		var differences int
		if err := db.Get(&differences, query); err != nil || differences != 0 {
			t.Fatalf("engine migration differences=%d: %v", differences, err)
		}
	}
}
