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

// DefaultControlPlaneCredentialProfiles returns the credential profiles to
// install for the built-in Agent on its first start. A credential binding row
// is an explicit administrator choice, including one with no Git profile, so
// it is never replaced by this bootstrap default.
func (s *Store) DefaultControlPlaneCredentialProfiles(ctx context.Context) (CredentialProfile, *CredentialProfile, bool, error) {
	var bindings int
	if err := s.DB.GetContext(ctx, &bindings, s.Q("SELECT COUNT(*) FROM server_credential_profiles WHERE server_id=?"), ControlPlaneServerID); err != nil {
		return CredentialProfile{}, nil, false, err
	}
	if bindings != 0 {
		return CredentialProfile{}, nil, false, nil
	}
	codex, err := s.preferredCredentialProfile(ctx, "codex")
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialProfile{}, nil, false, nil
	}
	if err != nil {
		return CredentialProfile{}, nil, false, err
	}
	git, err := s.preferredCredentialProfile(ctx, "git")
	if errors.Is(err, sql.ErrNoRows) {
		return codex, nil, true, nil
	}
	if err != nil {
		return CredentialProfile{}, nil, false, err
	}
	return codex, &git, true, nil
}

func (s *Store) preferredCredentialProfile(ctx context.Context, kind string) (CredentialProfile, error) {
	profile := CredentialProfile{}
	profileColumn := "codex_profile_id"
	if kind == "git" {
		profileColumn = "git_profile_id"
	}
	query := `SELECT p.id,p.kind,p.name,p.endpoint,p.username,p.model,p.commit_name,p.commit_email,p.ciphertext,p.updated_at
		FROM credential_profiles p
		WHERE p.kind=? AND p.ciphertext<>''
		ORDER BY CASE WHEN EXISTS (
			SELECT 1 FROM server_credential_profiles cp
			JOIN servers bound_server ON bound_server.id=cp.server_id
			WHERE bound_server.revoked_at IS NULL AND bound_server.is_control_plane=0 AND cp.` + profileColumn + `=p.id
		) THEN 0 ELSE 1 END,lower(p.name),p.id
		LIMIT 1`
	err := s.DB.GetContext(ctx, &profile, s.Q(query), kind)
	return profile, err
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
	resultData, err := operationResultData(result.Data)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=? WHERE id=?"), result.Status, result.Message, resultData, time.Now().UTC(), result.OperationID); err != nil {
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
