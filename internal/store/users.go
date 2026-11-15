package store

import (
	"context"
	"time"
)

type User struct {
	ID             string `db:"id" json:"id"`
	Username       string `db:"username" json:"username"`
	AuthMode       string `db:"auth_mode" json:"auth_mode"`
	PasswordHash   string `db:"password_hash" json:"-"`
	TOTPSecret     string `db:"totp_secret" json:"-"`
	RecoveryHashes string `db:"recovery_hashes" json:"-"`
}

const (
	AuthModePassword     = "password"
	AuthModeTOTP         = "totp"
	AuthModePasswordTOTP = "password_totp"
)

func (s *Store) HasUser(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.GetContext(ctx, &n, "SELECT COUNT(*) FROM users")
	return n > 0, err
}

func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO users(id,username,auth_mode,password_hash,totp_secret,recovery_hashes) VALUES(?,?,?,?,?,?)"), u.ID, u.Username, u.AuthMode, u.PasswordHash, u.TOTPSecret, u.RecoveryHashes)
	return err
}

func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	var u User
	err := s.DB.GetContext(ctx, &u, s.Q("SELECT id,username,auth_mode,password_hash,totp_secret,recovery_hashes FROM users WHERE username=?"), name)
	return u, err
}

func (s *Store) UserAuthMode(ctx context.Context) (string, error) {
	var mode string
	err := s.DB.GetContext(ctx, &mode, "SELECT auth_mode FROM users ORDER BY created_at LIMIT 1")
	return mode, err
}

type Session struct {
	ID        string    `db:"id"`
	UserID    string    `db:"user_id"`
	Username  string    `db:"username"`
	CSRFToken string    `db:"csrf_token"`
	ExpiresAt time.Time `db:"expires_at"`
}

func (s *Store) CreateSession(ctx context.Context, userID, tokenHash, csrf string, expires time.Time) error {
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at) VALUES(?,?,?,?,?)"), NewID(), userID, tokenHash, csrf, expires)
	return err
}

func (s *Store) SessionByToken(ctx context.Context, token string) (Session, error) {
	var session Session
	err := s.DB.GetContext(ctx, &session, s.Q(`SELECT s.id,s.user_id,u.username,s.csrf_token,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`), HashToken(token), time.Now().UTC())
	return session, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("DELETE FROM sessions WHERE token_hash=?"), HashToken(token))
	return err
}
