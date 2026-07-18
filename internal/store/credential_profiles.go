package store

import (
	"context"
	"database/sql"
	"time"
)

type CredentialProfile struct {
	ID         string    `db:"id" json:"id"`
	Kind       string    `db:"kind" json:"kind"`
	Name       string    `db:"name" json:"name"`
	Endpoint   string    `db:"endpoint" json:"endpoint"`
	Username   string    `db:"username" json:"username"`
	Model      string    `db:"model" json:"model"`
	Ciphertext string    `db:"ciphertext" json:"-"`
	UpdatedAt  time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) ListCredentialProfiles(ctx context.Context) ([]CredentialProfile, error) {
	var profiles []CredentialProfile
	err := s.DB.SelectContext(ctx, &profiles, "SELECT id,kind,name,endpoint,username,model,updated_at FROM credential_profiles ORDER BY kind,name")
	return profiles, err
}

func (s *Store) CredentialProfile(ctx context.Context, id string) (CredentialProfile, error) {
	var profile CredentialProfile
	err := s.DB.GetContext(ctx, &profile, s.Q("SELECT id,kind,name,endpoint,username,model,ciphertext,updated_at FROM credential_profiles WHERE id=?"), id)
	return profile, err
}

func (s *Store) SaveCredentialProfile(ctx context.Context, profile CredentialProfile, ciphertext string) (CredentialProfile, error) {
	now := time.Now().UTC()
	if profile.ID == "" {
		profile.ID = NewID()
		_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO credential_profiles(id,kind,name,endpoint,username,model,ciphertext,updated_at) VALUES(?,?,?,?,?,?,?,?)"), profile.ID, profile.Kind, profile.Name, profile.Endpoint, profile.Username, profile.Model, ciphertext, now)
		if err != nil {
			return CredentialProfile{}, err
		}
	} else {
		result, err := s.DB.ExecContext(ctx, s.Q("UPDATE credential_profiles SET kind=?,name=?,endpoint=?,username=?,model=?,ciphertext=CASE WHEN ?='' THEN ciphertext ELSE ? END,updated_at=? WHERE id=?"), profile.Kind, profile.Name, profile.Endpoint, profile.Username, profile.Model, ciphertext, ciphertext, now, profile.ID)
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
