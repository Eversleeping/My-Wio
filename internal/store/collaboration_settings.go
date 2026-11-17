package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/sessiontools"
)

// A reserved key uses the existing versioned grant storage. Negative effective
// versions keep queued requests authorized by legacy session grants distinct.
const globalCollaborationKey = "global"

func (s *Store) globalCollaboration(ctx context.Context, db sqlx.QueryerContext) (sessiontools.Grant, int, error) {
	var row struct {
		JSON    string `db:"grant_json"`
		Version int    `db:"version"`
	}
	err := sqlx.GetContext(ctx, db, &row, s.Q("SELECT grant_json,version FROM session_tool_grants WHERE source_key=?"), globalCollaborationKey)
	if errors.Is(err, sql.ErrNoRows) {
		return sessiontools.Grant{}, 0, nil
	}
	if err != nil {
		return sessiontools.Grant{}, 0, err
	}
	var grant sessiontools.Grant
	err = json.Unmarshal([]byte(row.JSON), &grant)
	return grant, row.Version, err
}

func (s *Store) CollaborationSettings(ctx context.Context) (sessiontools.Grant, error) {
	grant, _, err := s.globalCollaboration(ctx, s.DB)
	return grant, err
}

func (s *Store) SetCollaborationSettings(ctx context.Context, grant sessiontools.Grant) error {
	if len(grant.WorkspaceIDs) != 0 || len(grant.SessionRefs) != 0 {
		return sessiontools.Fail("INVALID_ARGUMENT", "global collaboration uses the source session's project and workspace")
	}
	for engine, mode := range grant.TargetModes {
		if !validCollaborationMode(engine, mode) {
			return sessiontools.Fail("INVALID_ARGUMENT", "invalid target permission mode")
		}
	}
	raw, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		previous, version, err := s.globalCollaboration(ctx, s.DB)
		if err != nil {
			return err
		}
		next := version + 1
		if version != 0 && collaborationScope(previous) == collaborationScope(grant) {
			next = version
		}
		result, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO session_tool_grants(source_key,grant_json,version,updated_at) VALUES(?,?,?,?) ON CONFLICT(source_key) DO UPDATE SET grant_json=excluded.grant_json,version=excluded.version,updated_at=excluded.updated_at WHERE session_tool_grants.version=?`), globalCollaborationKey, string(raw), next, time.Now().UTC(), version)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			return nil
		}
	}
	return errors.New("collaboration settings changed concurrently; reload and retry")
}

// Lock the global policy before per-session locks. New sessions need no local
// grant row, so this also serializes child limits and revocation for them.
func (s *Store) lockCollaborationGrant(ctx context.Context, tx *sqlx.Tx, key string) error {
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE session_tool_grants SET version=version WHERE source_key=?"), globalCollaborationKey); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.Q("UPDATE session_tool_grants SET version=version WHERE source_key=?"), key)
	return err
}
