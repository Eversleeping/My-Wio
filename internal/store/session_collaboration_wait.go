package store

import (
	"context"
	"time"

	"github.com/wio-platform/wio/internal/sessiontools"
)

func (s *Store) RegisterCollaborationWait(ctx context.Context, source sessiontools.Ref, targets []sessiontools.Ref, duration time.Duration) (time.Time, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE session_tool_wait_lock SET id=id WHERE id=1"); err != nil {
		return time.Time{}, err
	}
	var edges []struct {
		Source string `db:"source_key"`
		Target string `db:"target_key"`
	}
	if err = tx.SelectContext(ctx, &edges, s.Q("SELECT source_key,target_key FROM session_tool_waits WHERE expires_at>?"), time.Now().UTC()); err != nil {
		return time.Time{}, err
	}
	graph := map[string][]string{}
	for _, e := range edges {
		if e.Source == source.Key() {
			// A retry from the same session replaces its previous wait.
			continue
		}
		graph[e.Source] = append(graph[e.Source], e.Target)
	}
	for _, target := range targets {
		queue := []string{target.Key()}
		seen := map[string]bool{}
		for len(queue) > 0 {
			key := queue[0]
			queue = queue[1:]
			if key == source.Key() {
				return time.Time{}, sessiontools.Fail("WAIT_DEPENDENCY", "waiting would form a cycle")
			}
			if !seen[key] {
				seen[key] = true
				queue = append(queue, graph[key]...)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, s.Q("DELETE FROM session_tool_waits WHERE source_key=? OR expires_at<=?"), source.Key(), time.Now().UTC()); err != nil {
		return time.Time{}, err
	}
	expires := time.Now().UTC().Add(duration)
	for _, target := range targets {
		if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO session_tool_waits(source_key,target_key,expires_at) VALUES(?,?,?)"), source.Key(), target.Key(), expires); err != nil {
			return time.Time{}, err
		}
	}
	return expires, tx.Commit()
}

// ClearCollaborationWait keeps edges from a newer retry of the same source.
func (s *Store) ClearCollaborationWait(ctx context.Context, source sessiontools.Ref, expires time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = s.DB.ExecContext(ctx, s.Q("DELETE FROM session_tool_waits WHERE source_key=? AND expires_at<=?"), source.Key(), expires)
}
