package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
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
	return s.SetServerCredentialProfileIDs(ctx, serverID, codexProfileID, []string{gitProfileID})
}

func (s *Store) SetServerCredentialProfileIDs(ctx context.Context, serverID, codexProfileID string, gitProfileIDs []string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.setServerCredentialProfileIDs(ctx, tx, serverID, codexProfileID, gitProfileIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) setServerCredentialProfileIDs(ctx context.Context, tx *sqlx.Tx, serverID, codexProfileID string, gitProfileIDs []string) error {
	gitProfileIDs = normalizedProfileIDs(gitProfileIDs)
	primaryGitProfileID := ""
	if len(gitProfileIDs) > 0 {
		primaryGitProfileID = gitProfileIDs[0]
	}
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO server_credential_profiles(server_id,codex_profile_id,git_profile_id,updated_at) VALUES(?,?,NULLIF(?,''),?) ON CONFLICT(server_id) DO UPDATE SET codex_profile_id=excluded.codex_profile_id,git_profile_id=excluded.git_profile_id,updated_at=excluded.updated_at`), serverID, codexProfileID, primaryGitProfileID, time.Now().UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM server_git_credential_profiles WHERE server_id=?"), serverID); err != nil {
		return err
	}
	for position, profileID := range gitProfileIDs {
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO server_git_credential_profiles(server_id,profile_id,position) VALUES(?,?,?)"), serverID, profileID, position); err != nil {
			return err
		}
	}
	return nil
}

func normalizedProfileIDs(ids []string) []string {
	unique := make(map[string]bool, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || unique[id] {
			continue
		}
		unique[id] = true
		normalized = append(normalized, id)
	}
	return normalized
}

func (s *Store) QueueCredentialUpdate(ctx context.Context, serverID, ciphertext, codexProfileID string, gitProfileIDs []string, idempotency string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted operation payload must use a supported Vault format")
	}
	gitProfileIDs = normalizedProfileIDs(gitProfileIDs)
	gitProfileIDsJSON, err := json.Marshal(gitProfileIDs)
	if err != nil {
		return "", err
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
	primaryGitProfileID := ""
	if len(gitProfileIDs) > 0 {
		primaryGitProfileID = gitProfileIDs[0]
	}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO server_credential_updates(operation_id,server_id,codex_profile_id,git_profile_id,git_profile_ids,created_at) VALUES(?,?,?,NULLIF(?,''),?,?)"), operationID, serverID, codexProfileID, primaryGitProfileID, string(gitProfileIDsJSON), now); err != nil {
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
		var update struct {
			ServerID       string `db:"server_id"`
			CodexProfileID string `db:"codex_profile_id"`
			GitProfileIDs  string `db:"git_profile_ids"`
		}
		if err := tx.GetContext(ctx, &update, s.Q("SELECT server_id,COALESCE(codex_profile_id,'') codex_profile_id,git_profile_ids FROM server_credential_updates WHERE operation_id=?"), result.OperationID); err != nil {
			return err
		}
		var gitProfileIDs []string
		if err := json.Unmarshal([]byte(update.GitProfileIDs), &gitProfileIDs); err != nil {
			return fmt.Errorf("decode Git credential profile IDs: %w", err)
		}
		if err := s.setServerCredentialProfileIDs(ctx, tx, update.ServerID, update.CodexProfileID, gitProfileIDs); err != nil {
			return err
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
