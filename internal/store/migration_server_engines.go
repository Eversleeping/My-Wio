package store

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// Version 3 retains the legacy engine columns and bindings for recovery. Git
// remains independent; only the Codex portion of its old mixed binding moves.
func migrateServerEngines(ctx context.Context, tx *sqlx.Tx, _ string) error {
	for _, statement := range []string{
		`CREATE TABLE server_engines (
 server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
 engine TEXT NOT NULL CHECK(engine IN ('codex','gemini','claude')),
 cli_version TEXT NOT NULL DEFAULT '',
 ready INTEGER NOT NULL DEFAULT 0,
 credential_profile_id TEXT REFERENCES credential_profiles(id) ON DELETE SET NULL,
 profile_revision TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMP,
 PRIMARY KEY(server_id,engine)
)`,
		`INSERT INTO server_engines(server_id,engine,cli_version,ready,credential_profile_id,updated_at)
 SELECT s.id,'codex',s.codex_version,s.codex_ready,cp.codex_profile_id,cp.updated_at
 FROM servers s LEFT JOIN server_credential_profiles cp ON cp.server_id=s.id`,
		`INSERT INTO server_engines(server_id,engine,cli_version,ready,credential_profile_id,profile_revision,updated_at)
 SELECT s.id,'gemini',s.gemini_version,s.gemini_ready,cp.profile_id,COALESCE(cp.profile_revision,''),cp.updated_at
 FROM servers s LEFT JOIN server_gemini_credential_profiles cp ON cp.server_id=s.id`,
		`INSERT INTO server_engines(server_id,engine,cli_version,ready,credential_profile_id,profile_revision,model,updated_at)
 SELECT s.id,'claude',COALESCE(cli.version,''),COALESCE(cli.ready,0),cp.profile_id,COALESCE(cp.profile_revision,''),COALESCE(cp.model,''),cp.updated_at
 FROM servers s LEFT JOIN server_claude_cli cli ON cli.server_id=s.id LEFT JOIN server_claude_credential_profiles cp ON cp.server_id=s.id`,
		`CREATE INDEX server_engines_profile_idx ON server_engines(credential_profile_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
