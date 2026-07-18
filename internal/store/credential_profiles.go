package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type CredentialProfile struct {
	ID          string    `db:"id" json:"id"`
	Kind        string    `db:"kind" json:"kind"`
	Name        string    `db:"name" json:"name"`
	Endpoint    string    `db:"endpoint" json:"endpoint"`
	Username    string    `db:"username" json:"username"`
	Model       string    `db:"model" json:"model"`
	CommitName  string    `db:"commit_name" json:"commit_name"`
	CommitEmail string    `db:"commit_email" json:"commit_email"`
	Ciphertext  string    `db:"ciphertext" json:"-"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) SetServerCredentialProfiles(ctx context.Context, serverID, codexProfileID, gitProfileID string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO server_credential_profiles(server_id,codex_profile_id,git_profile_id,updated_at) VALUES(?,?,NULLIF(?,''),?) ON CONFLICT(server_id) DO UPDATE SET codex_profile_id=excluded.codex_profile_id,git_profile_id=excluded.git_profile_id,updated_at=excluded.updated_at`), serverID, codexProfileID, gitProfileID, time.Now().UTC())
	return err
}

func (s *Store) QueueCredentialUpdate(ctx context.Context, serverID, ciphertext, codexProfileID, gitProfileID, idempotency string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted operation payload must use a supported Vault format")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	operationID := NewID()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?)"), operationID, serverID, "credentials.configure", ciphertext, idempotency, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO server_credential_updates(operation_id,server_id,codex_profile_id,git_profile_id,created_at) VALUES(?,?,?,NULLIF(?,''),?)"), operationID, serverID, codexProfileID, gitProfileID, now); err != nil {
		return "", err
	}
	return operationID, tx.Commit()
}

func (s *Store) CompleteCredentialUpdate(ctx context.Context, result protocol.OperationResult) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=?,result=?,completed_at=? WHERE id=?"), result.Status, result.Message, time.Now().UTC(), result.OperationID); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		dbResult, err := tx.ExecContext(ctx, s.Q(`INSERT INTO server_credential_profiles(server_id,codex_profile_id,git_profile_id,updated_at) SELECT server_id,codex_profile_id,git_profile_id,? FROM server_credential_updates WHERE operation_id=? ON CONFLICT(server_id) DO UPDATE SET codex_profile_id=excluded.codex_profile_id,git_profile_id=excluded.git_profile_id,updated_at=excluded.updated_at`), time.Now().UTC(), result.OperationID)
		if err != nil {
			return err
		}
		rows, err := dbResult.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
	}
	return tx.Commit()
}

func (s *Store) ListCredentialProfiles(ctx context.Context) ([]CredentialProfile, error) {
	var profiles []CredentialProfile
	err := s.DB.SelectContext(ctx, &profiles, "SELECT id,kind,name,endpoint,username,model,commit_name,commit_email,updated_at FROM credential_profiles ORDER BY kind,name")
	return profiles, err
}

func (s *Store) CredentialProfile(ctx context.Context, id string) (CredentialProfile, error) {
	var profile CredentialProfile
	err := s.DB.GetContext(ctx, &profile, s.Q("SELECT id,kind,name,endpoint,username,model,commit_name,commit_email,ciphertext,updated_at FROM credential_profiles WHERE id=?"), id)
	return profile, err
}

func (s *Store) SaveCredentialProfile(ctx context.Context, profile CredentialProfile, ciphertext string) (CredentialProfile, error) {
	now := time.Now().UTC()
	if profile.ID == "" {
		profile.ID = NewID()
		_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO credential_profiles(id,kind,name,endpoint,username,model,commit_name,commit_email,ciphertext,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)"), profile.ID, profile.Kind, profile.Name, profile.Endpoint, profile.Username, profile.Model, profile.CommitName, profile.CommitEmail, ciphertext, now)
		if err != nil {
			return CredentialProfile{}, err
		}
	} else {
		result, err := s.DB.ExecContext(ctx, s.Q("UPDATE credential_profiles SET kind=?,name=?,endpoint=?,username=?,model=?,commit_name=?,commit_email=?,ciphertext=CASE WHEN ?='' THEN ciphertext ELSE ? END,updated_at=? WHERE id=?"), profile.Kind, profile.Name, profile.Endpoint, profile.Username, profile.Model, profile.CommitName, profile.CommitEmail, ciphertext, ciphertext, now, profile.ID)
		if err != nil {
			return CredentialProfile{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return CredentialProfile{}, err
		}
		if rows == 0 {
			return CredentialProfile{}, sql.ErrNoRows
		}
	}
	return s.CredentialProfile(ctx, profile.ID)
}

func (s *Store) DeleteCredentialProfile(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, s.Q("DELETE FROM credential_profiles WHERE id=?"), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}
