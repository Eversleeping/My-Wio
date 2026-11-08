package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

var fastTxRetryPolicy = txRetryPolicy{MaxAttempts: 4, BaseDelay: time.Microsecond, MaxDelay: 10 * time.Microsecond}

func pgError(code string) error { return &pgconn.PgError{Code: code, Message: "test"} }

func TestIsRetryableTxError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization failure", pgError("40001"), true},
		{"deadlock", pgError("40P01"), true},
		{"wrapped serialization failure", fmt.Errorf("add event: %w", pgError("40001")), true},
		{"unique violation", pgError("23505"), false},
		{"aborted transaction", pgError("25P02"), false},
		{"plain error", errors.New("40001"), false},
		{"no rows", sql.ErrNoRows, false},
		{"context canceled", context.Canceled, false},
	}
	for _, tc := range cases {
		if got := isRetryableTxError(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetryTxRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := retryTx(context.Background(), fastTxRetryPolicy, func() error {
		calls++
		if calls < 3 {
			return pgError("40001")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestRetryTxStopsAtMaxAttempts(t *testing.T) {
	calls := 0
	err := retryTx(context.Background(), fastTxRetryPolicy, func() error {
		calls++
		return pgError("40P01")
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != fastTxRetryPolicy.MaxAttempts {
		t.Fatalf("calls=%d, want %d", calls, fastTxRetryPolicy.MaxAttempts)
	}
}

func TestRetryTxDoesNotRetryOtherErrors(t *testing.T) {
	sentinel := errors.New("business rule")
	calls := 0
	err := retryTx(context.Background(), fastTxRetryPolicy, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestRetryTxHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryTx(ctx, fastTxRetryPolicy, func() error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestRetryTxStopsWhenContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := txRetryPolicy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour}
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- retryTx(ctx, policy, func() error {
			calls++
			return pgError("40001")
		})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retryTx did not return after cancellation")
	}
}

func TestTxRetryBackoffIsBoundedAndJittered(t *testing.T) {
	policy := txRetryPolicy{MaxAttempts: 10, BaseDelay: 4 * time.Millisecond, MaxDelay: 32 * time.Millisecond}
	for attempt := 1; attempt <= 12; attempt++ {
		ceiling := policy.BaseDelay << (attempt - 1)
		if attempt > 4 {
			ceiling = policy.MaxDelay
		}
		for i := 0; i < 200; i++ {
			got := policy.backoff(attempt)
			if got < ceiling/2 || got > ceiling {
				t.Fatalf("attempt %d: delay %v outside [%v,%v]", attempt, got, ceiling/2, ceiling)
			}
		}
	}
	if got := (txRetryPolicy{}).backoff(1); got != 0 {
		t.Fatalf("zero policy delay = %v", got)
	}
}

func TestInSerializableTxRollsBackBetweenAttempts(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	calls := 0
	err := db.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
		calls++
		if _, err := tx.ExecContext(ctx, db.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), "retry-event", "retry-stream", 1, "k", time.Now().UTC(), "{}"); err != nil {
			return err
		}
		if calls == 1 {
			return pgError("40001")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	var count int
	if err := db.DB.GetContext(ctx, &count, db.Q("SELECT COUNT(*) FROM events WHERE stream_id=?"), "retry-stream"); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestAddEventAllocatesSequencesAndKeepsDuplicates(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	for want := int64(1); want <= 3; want++ {
		event, err := db.AddEvent(ctx, protocol.StreamEvent{EventID: fmt.Sprintf("event-%d", want), StreamID: "stream", Kind: "k", Payload: []byte(`{}`)})
		if err != nil || event.Sequence != want {
			t.Fatalf("event %d: %+v %v", want, event, err)
		}
	}
	duplicate, err := db.AddEvent(ctx, protocol.StreamEvent{EventID: "event-2", StreamID: "stream", Kind: "k", Payload: []byte(`{}`)})
	if err != nil || duplicate.Sequence != 2 {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
}
