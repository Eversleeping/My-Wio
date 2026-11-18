package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/session"
)

// UpdateNativeSessionMetadata applies a single Claude or Gemini edit
// atomically. A pin-only edit still works while a turn is running.
func (s *Store) UpdateNativeSessionMetadata(ctx context.Context, ref session.Ref, title *string, archived, pinned *bool) error {
	if ref.Engine != session.Claude && ref.Engine != session.Gemini {
		return sql.ErrNoRows
	}
	if title == nil && archived == nil && pinned == nil {
		return nil
	}
	table := ref.Engine + "_threads"
	return s.inTx(ctx, nil, func(tx *sqlx.Tx) error {
		now := time.Now().UTC()
		var sets []string
		var args []any
		guard := ""
		if title != nil || archived != nil {
			guard = " AND status NOT IN ('queued','running','interrupting') AND (status<>'interrupted' OR NOT EXISTS (SELECT 1 FROM agent_operations WHERE id=" + table + ".operation_id AND status IN ('queued','delivered','running'))) AND NOT EXISTS (SELECT 1 FROM task_session_bindings b WHERE b.engine='" + ref.Engine + "' AND b.session_id=" + table + ".id AND b.released_at IS NULL)"
			sets, args = append(sets, "updated_at=?"), append(args, now)
		}
		if title != nil {
			sets, args = append(sets, "title=?"), append(args, *title)
		}
		if archived != nil {
			var archivedAt *time.Time
			if *archived {
				archivedAt = &now
			}
			sets, args = append(sets, "archived_at=?"), append(args, archivedAt)
		}
		var pinnedAt *time.Time
		if pinned != nil && *pinned {
			pinnedAt = &now
		}
		if pinned != nil && ref.Engine == session.Gemini {
			sets, args = append(sets, "pinned_at=?"), append(args, pinnedAt)
		}
		if len(sets) == 0 {
			// Hold the native row through the separate Claude metadata write.
			sets = []string{"id=id"}
		}
		result, err := tx.ExecContext(ctx, s.Q("UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE id=?"+guard), append(args, ref.ID)...)
		if err != nil {
			return err
		}
		if err := s.sessionMutationResult(ctx, tx, table, ref.ID, result); err != nil {
			return err
		}
		if pinned != nil && ref.Engine == session.Claude {
			_, err = tx.ExecContext(ctx, s.Q("INSERT INTO claude_thread_management(thread_id,pinned_at) VALUES(?,?) ON CONFLICT(thread_id) DO UPDATE SET pinned_at=excluded.pinned_at"), ref.ID, pinnedAt)
		}
		return err
	})
}

func (s *Store) sessionMutationResult(ctx context.Context, tx *sqlx.Tx, table, id string, result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil || n == 1 {
		return err
	}
	var existing string
	if err := tx.GetContext(ctx, &existing, s.Q("SELECT id FROM "+table+" WHERE id=?"), id); err != nil {
		return err
	}
	return ErrThreadActive
}

// Keep historical requests and links needed by sessions forked from this one.
func (s *Store) clearDeletedSessionRecords(ctx context.Context, tx *sqlx.Tx, engine, id string) error {
	key := engine + ":" + id
	for _, table := range []string{"session_subscriptions", "session_tool_waits"} {
		if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM "+table+" WHERE target_key=? OR source_key=?"), key, key); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM session_links WHERE target_key=?"), key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM session_tool_grants WHERE source_key=?"), key); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=?"), id)
	return err
}
