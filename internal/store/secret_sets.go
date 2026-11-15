package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type SecretSet struct {
	ID         string    `db:"id" json:"id"`
	Name       string    `db:"name" json:"name"`
	KeyVersion int       `db:"key_version" json:"key_version"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) ListSecretSets(ctx context.Context) ([]SecretSet, error) {
	var out []SecretSet
	err := s.DB.SelectContext(ctx, &out, "SELECT id,name,key_version,updated_at FROM secret_sets ORDER BY name")
	return out, err
}

func (s *Store) UpsertSecretSet(ctx context.Context, id, name, ciphertext string) (string, error) {
	var resultID string
	err := s.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
		setID := id
		var current struct {
			ID         string `db:"id"`
			KeyVersion int    `db:"key_version"`
		}
		lookup := "SELECT id,key_version FROM secret_sets WHERE name=?"
		args := []any{name}
		if strings.TrimSpace(id) != "" {
			lookup = "SELECT id,key_version FROM secret_sets WHERE id=? OR name=? ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1"
			args = []any{id, name, id}
		}
		err := tx.GetContext(ctx, &current, s.Q(lookup), args...)
		now := time.Now().UTC()
		version := 1
		if errors.Is(err, sql.ErrNoRows) {
			if setID == "" {
				setID = NewID()
			}
			if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO secret_sets(id,name,ciphertext,key_version,created_at,updated_at) VALUES(?,?,?,?,?,?)"), setID, name, ciphertext, version, now, now); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			setID = current.ID
			version = current.KeyVersion + 1
			if _, err = tx.ExecContext(ctx, s.Q("UPDATE secret_sets SET name=?,ciphertext=?,key_version=?,updated_at=? WHERE id=?"), name, ciphertext, version, now, setID); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO secret_set_versions(secret_set_id,key_version,ciphertext,updated_at) VALUES(?,?,?,?)"), setID, version, ciphertext, now); err != nil {
			return err
		}
		resultID = setID
		return nil
	})
	if err != nil {
		return "", err
	}
	return resultID, nil
}

func (s *Store) SecretCiphertext(ctx context.Context, id string) (string, error) {
	var value string
	err := s.DB.GetContext(ctx, &value, s.Q("SELECT ciphertext FROM secret_sets WHERE id=?"), id)
	return value, err
}

func (s *Store) SecretCiphertextVersion(ctx context.Context, id string, version int) (string, time.Time, error) {
	var value struct {
		Ciphertext string    `db:"ciphertext"`
		UpdatedAt  time.Time `db:"updated_at"`
	}
	err := s.DB.GetContext(ctx, &value, s.Q("SELECT ciphertext,updated_at FROM secret_set_versions WHERE secret_set_id=? AND key_version=?"), id, version)
	return value.Ciphertext, value.UpdatedAt, err
}
