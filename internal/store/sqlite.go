package store

import (
	"context"
	"database/sql/driver"
	"net/url"
	"strings"
)

func sqliteDSN(dsn string) (string, error) {
	if dsn == "" {
		dsn = "wio.db"
	}
	name, query, _ := strings.Cut(dsn, "?")
	options, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	pragmas := options["_pragma"]
	delete(options, "_pragma")
	busyTimeout := false
	for _, pragma := range pragmas {
		key, _, _ := strings.Cut(strings.TrimSpace(pragma), "(")
		key, _, _ = strings.Cut(key, "=")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "busy_timeout":
			busyTimeout = true
		case "foreign_keys":
			continue
		}
		options.Add("_pragma", pragma)
	}
	if !busyTimeout {
		options.Add("_pragma", "busy_timeout(5000)")
	}
	options.Add("_pragma", "foreign_keys(1)")
	return name + "?" + options.Encode(), nil
}

type sqliteConnector struct {
	dsn    string
	driver driver.Driver
}

func (c sqliteConnector) Driver() driver.Driver { return c.driver }

type sqliteConnection interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
}

func (c sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return sqliteQueryConnection{conn.(sqliteConnection)}, nil
}

type sqliteQueryConnection struct{ sqliteConnection }

func (c sqliteQueryConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return sqliteQuery(ctx, func(queryCtx context.Context) (driver.Rows, error) {
		return c.sqliteConnection.QueryContext(queryCtx, query, args)
	})
}

func (c sqliteQueryConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.sqliteConnection.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return sqliteQueryStatement{stmt.(sqliteStatement)}, nil
}

func (c sqliteQueryConnection) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

type sqliteStatement interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
}

type sqliteQueryStatement struct{ sqliteStatement }

func (s sqliteQueryStatement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return sqliteQuery(ctx, func(queryCtx context.Context) (driver.Rows, error) {
		return s.sqliteStatement.QueryContext(queryCtx, args)
	})
}

// modernc SQLite can discard open rows if cancellation races with the first
// result. Take ownership of the rows before observing cancellation. SQLite's
// busy timeout still bounds lock waits, and database/sql can cancel iteration.
func sqliteQuery(ctx context.Context, query func(context.Context) (driver.Rows, error)) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := query(context.WithoutCancel(ctx))
	if cancelled := ctx.Err(); cancelled != nil {
		if rows != nil {
			_ = rows.Close()
		}
		return nil, cancelled
	}
	return rows, err
}
