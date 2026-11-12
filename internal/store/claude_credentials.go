package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrClaudeCredentialsBusy = errors.New("finish active Claude turns, installations and configuration changes first")

// ClaudeModelConfig identifies the successfully applied credential snapshot.
// It contains binding metadata only, never a key or key hash.
type ClaudeModelConfig struct {
	Revision        string    `db:"revision"`
	ProfileID       string    `db:"profile_id"`
	ProfileRevision string    `db:"profile_revision"`
	AppliedAt       time.Time `db:"updated_at"`
}

func (s *Store) ClaudeModelConfiguration(ctx context.Context, serverID string) (ClaudeModelConfig, error) {
	var config ClaudeModelConfig
	err := s.DB.GetContext(ctx, &config, s.Q(`SELECT `+claudeModelRevision+` AS revision,COALESCE(ccp.profile_id,'') AS profile_id,ccp.profile_revision,ccp.updated_at FROM server_claude_credential_profiles ccp LEFT JOIN credential_profiles claude ON claude.id=ccp.profile_id WHERE ccp.server_id=?`), serverID)
	return config, err
}

// Recover the encrypted command for the successfully applied revision, never
// an unapplied or failed update. This lets model discovery distinguish a model
// edit from a change to the endpoint or key, including edits made before upgrade.
func (s *Store) AppliedClaudeCredentialPayload(ctx context.Context, serverID string, config ClaudeModelConfig) (string, error) {
	var payload string
	err := s.DB.GetContext(ctx, &payload, s.Q(`SELECT o.payload FROM server_claude_credential_updates u JOIN agent_operations o ON o.id=u.operation_id
 WHERE u.server_id=? AND u.profile_id=? AND u.profile_revision=? AND o.status='succeeded' AND o.completed_at<=?
 ORDER BY o.completed_at DESC LIMIT 1`), serverID, config.ProfileID, config.ProfileRevision, config.AppliedAt)
	return payload, err
}

func (s *Store) QueueClaudeCredentialUpdate(ctx context.Context, serverID, profileID, ciphertext, model string, profileUpdatedAt ...time.Time) (string, error) {
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
	if err = tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind IN ('claude.turn.start','claude.credentials.configure','cli.install','agent.update') AND status IN ('queued','delivered','running')`), serverID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrClaudeCredentialsBusy
	}
	var sourceUpdatedAt time.Time
	if len(profileUpdatedAt) > 0 {
		sourceUpdatedAt = profileUpdatedAt[0]
	} else if err := tx.GetContext(ctx, &sourceUpdatedAt, s.Q("SELECT updated_at FROM credential_profiles WHERE id=?"), profileID); err != nil {
		return "", err
	}
	sourceRevision := sourceUpdatedAt.UTC().Format(time.RFC3339Nano)
	id := NewID()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,'claude.credentials.configure',?,?,?)`), id, serverID, ciphertext, "claude-credentials:"+id, time.Now().UTC()); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_claude_credential_updates(operation_id,server_id,profile_id,profile_revision,model) VALUES(?,?,?,?,?)`), id, serverID, profileID, sourceRevision, model); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *Store) CompleteClaudeCredentialUpdate(ctx context.Context, result protocol.OperationResult) error {
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
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_claude_credential_profiles(server_id,profile_id,updated_at,profile_revision,model)
		 SELECT server_id,profile_id,?,profile_revision,model FROM server_claude_credential_updates WHERE operation_id=?
		 ON CONFLICT(server_id) DO UPDATE SET profile_id=excluded.profile_id,updated_at=excluded.updated_at,profile_revision=excluded.profile_revision,model=excluded.model`), time.Now().UTC(), result.OperationID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
