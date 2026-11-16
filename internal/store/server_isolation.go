package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/wio-platform/wio/internal/protocol"
)

// Version 6 only extends storage. Existing capability and server rows remain
// intact, and old heartbeats clear an unknown/stale isolation declaration.
func migrateSessionIsolation(ctx context.Context, tx *sqlx.Tx, _ string) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE server_session_isolation (
 server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
 status TEXT NOT NULL
 )`)
	return err
}

const serverIsolationSelect = "COALESCE((SELECT status FROM server_session_isolation WHERE server_id=s.id),'') AS session_isolation_json"

func normalizeIsolationStatus(status protocol.SessionIsolationStatus) protocol.SessionIsolationStatus {
	switch status.Mode {
	case protocol.IsolationDisabled, protocol.IsolationEnabled:
		status.Reason = ""
	case protocol.IsolationUnsupported:
		status.Reason = strings.TrimSpace(status.Reason)
		if len(status.Reason) > 1024 {
			status.Reason = status.Reason[:1024]
		}
	default:
		status = protocol.SessionIsolationStatus{Mode: protocol.IsolationUnsupported, Reason: "Agent reported an unknown session isolation state"}
	}
	return status
}

func saveIsolationStatus(ctx context.Context, tx *sqlx.Tx, query func(string) string, serverID string, status *protocol.SessionIsolationStatus) error {
	if status == nil {
		_, err := tx.ExecContext(ctx, query("DELETE FROM server_session_isolation WHERE server_id=?"), serverID)
		return err
	}
	data, err := json.Marshal(normalizeIsolationStatus(*status))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, query(`INSERT INTO server_session_isolation(server_id,status) SELECT id,? FROM servers WHERE id=? AND revoked_at IS NULL ON CONFLICT(server_id) DO UPDATE SET status=excluded.status`), string(data), serverID)
	return err
}

func (s *Server) loadIsolationStatus() {
	if s.IsolationJSON == "" {
		return
	}
	var status protocol.SessionIsolationStatus
	if json.Unmarshal([]byte(s.IsolationJSON), &status) != nil {
		return
	}
	status = normalizeIsolationStatus(status)
	s.SessionIsolation = &status
}
