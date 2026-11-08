package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// PostgreSQL SQLSTATEs that mean "the transaction lost a race; re-running it
// from the start is expected to succeed".
const (
	pgSerializationFailure = "40001"
	pgDeadlockDetected     = "40P01"
)

// txRetryPolicy bounds how often and how quickly a transaction is re-run.
type txRetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// defaultTxRetryPolicy keeps the worst-case added latency well under a second
// while surviving bursts of concurrent agent events.
var defaultTxRetryPolicy = txRetryPolicy{MaxAttempts: 10, BaseDelay: 2 * time.Millisecond, MaxDelay: 100 * time.Millisecond}

// isRetryableTxError reports whether err is a PostgreSQL serialization failure
// or deadlock. Other drivers (SQLite) never produce these errors.
func isRetryableTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgSerializationFailure || pgErr.Code == pgDeadlockDetected
}

// backoff returns a jittered delay for the given 1-based failed attempt:
// a uniformly random value in [delay/2, delay] where delay grows exponentially
// from BaseDelay up to MaxDelay.
func (p txRetryPolicy) backoff(attempt int) time.Duration {
	delay := p.BaseDelay
	for i := 1; i < attempt && delay < p.MaxDelay; i++ {
		delay *= 2
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if delay <= 0 {
		return 0
	}
	half := delay / 2
	return half + time.Duration(rand.Int63n(int64(delay-half)+1))
}

// retryTx runs attempt until it succeeds, fails with a non-retryable error,
// the attempt budget is exhausted or ctx is done. attempt must be safe to
// re-run from scratch: it must not have side effects outside the database
// transaction it opens, and must not carry state between invocations.
func retryTx(ctx context.Context, policy txRetryPolicy, attempt func() error) error {
	maxAttempts := policy.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for n := 1; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := attempt()
		if err == nil || !isRetryableTxError(err) || n >= maxAttempts {
			return err
		}
		timer := time.NewTimer(policy.backoff(n))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// inTx runs fn inside a transaction with the given options and commits when
// fn returns nil. On PostgreSQL serialization failures and deadlocks (raised
// by any statement or by COMMIT) the whole transaction, including fn, is
// re-run. fn must therefore only communicate results by assigning variables
// that it fully (re)initialises on every call.
func (s *Store) inTx(ctx context.Context, opts *sql.TxOptions, fn func(tx *sqlx.Tx) error) error {
	return retryTx(ctx, defaultTxRetryPolicy, func() error {
		tx, err := s.DB.BeginTxx(ctx, opts)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// inSerializableTx runs fn in a SERIALIZABLE transaction with retry; see inTx.
func (s *Store) inSerializableTx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	return s.inTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, fn)
}

func (s *Store) isPostgres() bool { return s.driver == "pgx" }

// eventStreamLockNamespace is the first key of the two-key PostgreSQL advisory
// lock taken per event stream ("WIO\x01"), keeping it apart from any other
// advisory locks in the database.
const eventStreamLockNamespace = 0x57494f01

// maxEventSequenceAttempts bounds in-transaction re-allocation when a writer
// outside the stream lock claims a sequence between MAX and INSERT.
const maxEventSequenceAttempts = 8

// addEventTxOptions selects AddEvent's isolation level. On PostgreSQL it uses
// READ COMMITTED under a per-stream advisory lock: every statement gets a fresh
// snapshot, so MAX(sequence) observes the commit of the previous lock holder.
// SERIALIZABLE would not work with the lock because its snapshot is taken
// before the lock is granted, and its predicate locks also make unrelated
// streams conflict. SQLite has a single connection and keeps its behaviour.
func (s *Store) addEventTxOptions() *sql.TxOptions {
	if s.isPostgres() {
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return &sql.TxOptions{Isolation: sql.LevelSerializable}
}

// lockEventStream serializes appends to one event stream until the end of the
// transaction. PostgreSQL only.
func lockEventStream(ctx context.Context, tx *sqlx.Tx, streamID string) error {
	_, err := tx.ExecContext(ctx, tx.Rebind("SELECT pg_advisory_xact_lock(?, hashtext(?))"), eventStreamLockNamespace, streamID)
	return err
}
