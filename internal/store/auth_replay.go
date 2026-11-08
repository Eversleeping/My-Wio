package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// recoveryConsumeAttempts bounds the compare-and-set retries in
// ConsumeRecoveryCode. Every lost race means another login removed a code, and
// there are at most a handful of codes, so this is only reached on corruption.
const recoveryConsumeAttempts = 16

// migrateUserTOTPCounter adds the column that makes a TOTP code single-use.
// Zero is older than every real time step, so existing users are unaffected.
func migrateUserTOTPCounter(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, "ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_last_counter BIGINT NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("migrate user TOTP counter: %w", err)
		}
		return nil
	}
	var count int
	if err := db.GetContext(ctx, &count, "SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='totp_last_counter'"); err != nil {
		return fmt.Errorf("inspect user TOTP counter: %w", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE users ADD COLUMN totp_last_counter BIGINT NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("migrate user TOTP counter: %w", err)
		}
	}
	return nil
}

// AdvanceTOTPCounter atomically records counter as the user's last accepted
// TOTP time step. It returns false when the counter is not newer than the
// stored one, meaning the code was already used.
func (s *Store) AdvanceTOTPCounter(ctx context.Context, userID string, counter int64) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE users SET totp_last_counter=? WHERE id=? AND totp_last_counter<?"), counter, userID, counter)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// ConsumeRecoveryCode removes hash from the user's recovery codes and reports
// whether it was present. The write is a compare-and-set on the value read, so
// concurrent logins can neither both spend one code nor restore a code that a
// parallel login just removed.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	for attempt := 0; attempt < recoveryConsumeAttempts; attempt++ {
		var stored string
		err := s.DB.GetContext(ctx, &stored, s.Q("SELECT recovery_hashes FROM users WHERE id=?"), userID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var hashes []string
		if err := json.Unmarshal([]byte(stored), &hashes); err != nil {
			return false, fmt.Errorf("decode recovery codes: %w", err)
		}
		index := -1
		for i, candidate := range hashes {
			if candidate == hash {
				index = i
				break
			}
		}
		if index < 0 {
			return false, nil
		}
		remaining := make([]string, 0, len(hashes)-1)
		remaining = append(remaining, hashes[:index]...)
		remaining = append(remaining, hashes[index+1:]...)
		updated, err := json.Marshal(remaining)
		if err != nil {
			return false, err
		}
		result, err := s.DB.ExecContext(ctx, s.Q("UPDATE users SET recovery_hashes=? WHERE id=? AND recovery_hashes=?"), string(updated), userID, stored)
		if err != nil {
			return false, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if rows == 1 {
			return true, nil
		}
	}
	return false, errors.New("recovery codes changed too many times during sign-in")
}
