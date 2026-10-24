package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrGeminiCredentialsBusy = errors.New("finish active Gemini turns, installations and credential updates first")

func (s *Store) QueueGeminiCredentialUpdate(ctx context.Context, serverID, profileID, ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted credential payload required")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE servers SET status=status WHERE id=?"), serverID); err != nil {
		return "", err
	}
	var active int
	if err = tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('gemini.turn.start','gemini.credentials.configure','cli.install','agent.update') AND status IN ('queued','delivered','running')`), serverID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrGeminiCredentialsBusy
	}
	id := NewID()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,'gemini.credentials.configure',?,?,?)`), id, serverID, ciphertext, "gemini-credentials:"+id, time.Now().UTC()); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_gemini_credential_updates(operation_id,server_id,profile_id) VALUES(?,?,?)`), id, serverID, profileID); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *Store) CompleteGeminiCredentialUpdate(ctx context.Context, result protocol.OperationResult) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Duplicate delivery must not reapply an older profile after a newer update.
	updated, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status=?,result=?,completed_at=? WHERE id=? AND status IN ('queued','delivered','running')`), result.Status, result.Message, time.Now().UTC(), result.OperationID)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count != 0 && result.Status == "succeeded" {
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_gemini_credential_profiles(server_id,profile_id,updated_at)
		 SELECT server_id,profile_id,? FROM server_gemini_credential_updates WHERE operation_id=?
		 ON CONFLICT(server_id) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at`), time.Now().UTC(), result.OperationID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
