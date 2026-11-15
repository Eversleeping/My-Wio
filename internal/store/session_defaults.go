package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/wio-platform/wio/internal/session"
)

// WorkspaceSessionDefaults reads the first active session in each native
// engine's existing pin/update order, independently of any UI cursor window.
func (s *Store) WorkspaceSessionDefaults(ctx context.Context, workspaceID string) (map[string]AISession, error) {
	result := map[string]AISession{}
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		var row AISession
		err := s.DB.GetContext(ctx, &row, s.Q("SELECT * FROM ("+aiSessionRows+") sessions WHERE workspace_id=? AND engine=? AND archived_at IS NULL AND server_revoked_at IS NULL ORDER BY "+aiSessionOrder+" LIMIT 1"), workspaceID, engine)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[engine] = row
	}
	return result, nil
}
