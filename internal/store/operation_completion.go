package store

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

// completeActiveOperation records the terminal status of an operation that is
// still queued, delivered or running, inside the caller's transaction. It
// returns ErrOperationNotActive when the operation already finished, for
// example because it was cancelled or failed by a reconcile while its result
// was in flight; the caller must then roll back and skip the side effects of
// the stale result. Claim the operation before applying side effects so a
// concurrent completion of the same operation waits on its row lock.
func (s *Store) completeActiveOperation(ctx context.Context, tx *sqlx.Tx, id, status, message, data string, at time.Time) error {
	result, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=? WHERE id=? AND status IN ('queued','delivered','running')"), status, message, data, at, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrOperationNotActive
	}
	return nil
}
