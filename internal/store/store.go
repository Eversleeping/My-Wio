package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type Store struct {
	DB     *sqlx.DB
	driver string
}

type OpenOptions struct {
	// PostgresMaxOpenConns defaults to 20. SQLite always uses one connection.
	PostgresMaxOpenConns int
}

func Open(databaseURL string) (*Store, error) {
	return OpenWithOptions(databaseURL, OpenOptions{})
}

func OpenWithOptions(databaseURL string, options OpenOptions) (*Store, error) {
	if options.PostgresMaxOpenConns < 0 {
		return nil, errors.New("PostgresMaxOpenConns must be positive")
	}
	if options.PostgresMaxOpenConns == 0 {
		options.PostgresMaxOpenConns = 20
	}
	driver, dsn := "sqlite", databaseURL
	var db *sqlx.DB
	var err error
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		driver = "pgx"
		config, parseErr := pgx.ParseConfig(databaseURL)
		if parseErr != nil {
			return nil, parseErr
		}
		// These are short, interactive queries. Compilation can take seconds
		// when estimates for small, sparsely analyzed tables trigger PostgreSQL
		// JIT. Apply this to every pooled connection, including replacements.
		config.RuntimeParams["jit"] = "off"
		db = sqlx.NewDb(stdlib.OpenDB(*config), driver)
	} else if dsn == "" {
		dsn = "wio.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	if db == nil {
		db, err = sqlx.Open(driver, dsn)
	}
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(options.PostgresMaxOpenConns)
		db.SetMaxIdleConns(min(5, options.PostgresMaxOpenConns))
		db.SetConnMaxIdleTime(5 * time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	baselineApplied, err := runMigrations(ctx, db, driver, schemaMigrations)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !baselineApplied {
		if err := (&Store{DB: db, driver: driver}).purgeRevokedServers(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("purge revoked servers: %w", err)
		}
	}
	return &Store{DB: db, driver: driver}, nil
}

func (s *Store) Close() error      { return s.DB.Close() }
func (s *Store) Q(q string) string { return s.DB.Rebind(q) }
func NewID() string                { return uuid.NewString() }

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
