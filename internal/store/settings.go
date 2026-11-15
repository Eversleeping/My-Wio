package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) Setting(ctx context.Context, key, fallback string) (string, error) {
	var value string
	err := s.DB.GetContext(ctx, &value, s.Q("SELECT value FROM control_settings WHERE key=?"), key)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return value, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO control_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`), key, value, time.Now().UTC())
	return err
}
