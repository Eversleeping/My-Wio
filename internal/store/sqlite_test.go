package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestSQLiteOptionsApplyToReplacementConnections(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "options.db") + "?_pragma=busy_timeout(1234)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.DB.SetMaxIdleConns(0)

	for _, check := range []struct {
		query    string
		expected int
	}{
		{"PRAGMA foreign_keys", 1},
		{"PRAGMA busy_timeout", 1234},
	} {
		var value int
		if err := db.DB.Get(&value, check.query); err != nil || value != check.expected {
			t.Fatalf("%s = %d, want %d: %v", check.query, value, check.expected, err)
		}
	}
}

type cancelledSQLiteRows struct{ closed bool }

func (r *cancelledSQLiteRows) Columns() []string         { return []string{"result"} }
func (r *cancelledSQLiteRows) Next([]driver.Value) error { return io.EOF }
func (r *cancelledSQLiteRows) Close() error              { r.closed = true; return nil }

func TestSQLiteQueryClosesRowsWhenRequestIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := &cancelledSQLiteRows{}
	rows, err := sqliteQuery(ctx, func(queryCtx context.Context) (driver.Rows, error) {
		cancel()
		if queryCtx.Err() != nil {
			t.Fatal("driver could discard its rows after cancellation")
		}
		return result, nil
	})
	if rows != nil || !errors.Is(err, context.Canceled) || !result.closed {
		t.Fatalf("cancelled query did not finalize its rows: rows=%v err=%v closed=%v", rows, err, result.closed)
	}
}
