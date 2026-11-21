package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type ServerRegistration struct {
	ID               string           `db:"id" json:"id"`
	Name             string           `db:"name" json:"name"`
	CodeHash         string           `db:"code_hash" json:"-"`
	DeviceHash       string           `db:"device_hash" json:"-"`
	ConfigJSON       string           `db:"config_json" json:"-"`
	State            string           `db:"state" json:"state"`
	ServerID         string           `db:"server_id" json:"server_id"`
	ProgressJSON     RegistrationJSON `db:"progress_json" json:"progress"`
	ResultJSON       RegistrationJSON `db:"result_json" json:"result"`
	ErrorCode        string           `db:"error_code" json:"error_code"`
	CreatedAt        time.Time        `db:"created_at" json:"created_at"`
	ExpiresAt        time.Time        `db:"expires_at" json:"expires_at"`
	SessionExpiresAt time.Time        `db:"session_expires_at" json:"session_expires_at"`
}

func (s *Store) CreateServerRegistration(ctx context.Context, name, code, configuration string) (ServerRegistration, error) {
	now := time.Now().UTC()
	r := ServerRegistration{ID: NewID(), Name: name, State: "pending", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), SessionExpiresAt: now.Add(15 * time.Minute), ProgressJSON: RegistrationJSON(`[]`), ResultJSON: RegistrationJSON(`{}`)}
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO server_registrations(id,name,code_hash,config_json,created_at,expires_at,session_expires_at) VALUES(?,?,?,?,?,?,?)`), r.ID, name, HashToken(code), configuration, now, r.ExpiresAt, r.SessionExpiresAt)
	return r, err
}
func (s *Store) ServerRegistration(ctx context.Context, id string) (ServerRegistration, error) {
	var r ServerRegistration
	err := s.DB.GetContext(ctx, &r, s.Q(`SELECT * FROM server_registrations WHERE id=?`), id)
	return r, err
}
func (s *Store) ListServerRegistrations(ctx context.Context) ([]ServerRegistration, error) {
	rows := []ServerRegistration{}
	err := s.DB.SelectContext(ctx, &rows, `SELECT * FROM server_registrations ORDER BY created_at DESC LIMIT 30`)
	return rows, err
}
func (s *Store) ClaimServerRegistration(ctx context.Context, id, code, device string) (ServerRegistration, error) {
	if len(device) < 32 || len(device) > 256 {
		return ServerRegistration{}, sql.ErrNoRows
	}
	now := time.Now().UTC()
	// Compare-and-swap is the ownership boundary on both SQLite and PostgreSQL.
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE server_registrations SET device_hash=?,state='installing',session_expires_at=? WHERE id=? AND code_hash=? AND device_hash='' AND state='pending' AND expires_at>?`), HashToken(device), now.Add(time.Hour), id, HashToken(code), now)
	if err != nil {
		return ServerRegistration{}, err
	}
	return s.AuthenticateServerRegistration(ctx, id, device)
}
func (s *Store) AuthenticateServerRegistration(ctx context.Context, id, device string) (ServerRegistration, error) {
	r, err := s.ServerRegistration(ctx, id)
	if err != nil {
		return r, err
	}
	if device == "" || r.DeviceHash == "" || subtle.ConstantTimeCompare([]byte(r.DeviceHash), []byte(HashToken(device))) != 1 || r.State == "revoked" || !r.SessionExpiresAt.After(time.Now().UTC()) {
		return ServerRegistration{}, sql.ErrNoRows
	}
	return r, nil
}
func (s *Store) RevokeServerRegistration(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, s.Q(`UPDATE server_registrations SET state='revoked' WHERE id=? AND state<>'complete'`), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return sql.ErrNoRows
	}
	return err
}

// EnrollServerRegistration commits the server and its registration link together.
// The supplied token is stable for this device, so retries recover the same
// credentials without storing plaintext Agent tokens in the database.
func (s *Store) EnrollServerRegistration(ctx context.Context, id, device, hostname, agentToken string, e Enrollment) (string, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	// Lock with a conditional write before reading; concurrent enrolls serialize.
	result, err := tx.ExecContext(ctx, s.Q(`UPDATE server_registrations SET state='registered' WHERE id=? AND device_hash=? AND state IN ('installing','registered','failed') AND session_expires_at>?`), id, HashToken(device), now)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", sql.ErrNoRows
	}
	var serverID string
	if err := tx.GetContext(ctx, &serverID, s.Q(`SELECT server_id FROM server_registrations WHERE id=?`), id); err != nil {
		return "", err
	}
	if serverID != "" {
		var count int
		if err := tx.GetContext(ctx, &count, s.Q(`SELECT COUNT(*) FROM servers s JOIN agent_credentials a ON a.server_id=s.id WHERE s.id=? AND s.revoked_at IS NULL AND a.revoked_at IS NULL AND a.token_hash=?`), serverID, HashToken(agentToken)); err != nil {
			return "", err
		}
		if count != 1 {
			return "", sql.ErrNoRows
		}
		return serverID, tx.Commit()
	}
	serverID = NewID()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO servers(id,name,hostname,scan_roots) VALUES(?,?,?,?)`), serverID, e.ServerName, hostname, e.ScanRoots); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_metadata(server_id,address,configuration,notes) VALUES(?,?,?,?)`), serverID, e.Address, e.Configuration, e.Notes); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_credentials(server_id,token_hash) VALUES(?,?)`), serverID, HashToken(agentToken)); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE server_registrations SET server_id=? WHERE id=?`), serverID, id); err != nil {
		return "", err
	}
	return serverID, tx.Commit()
}

func (s *Store) UpdateServerRegistrationProgress(ctx context.Context, id, device, state, progress, resultJSON, errorCode string) error {
	result, err := s.DB.ExecContext(ctx, s.Q(`UPDATE server_registrations SET state=?,progress_json=?,result_json=?,error_code=? WHERE id=? AND device_hash=? AND state NOT IN ('revoked','complete') AND session_expires_at>?`), state, progress, resultJSON, errorCode, id, HashToken(device), time.Now().UTC())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return sql.ErrNoRows
	}
	return err
}
func (s *Store) CompleteServerRegistration(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, s.Q(`UPDATE server_registrations SET state='complete' WHERE id=? AND state='waiting_online'`), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return sql.ErrNoRows
	}
	return err
}

// RegistrationJSON scans the TEXT representation used by both SQL drivers.
type RegistrationJSON []byte

func (j *RegistrationJSON) Scan(value any) error {
	switch v := value.(type) {
	case string:
		*j = []byte(v)
	case []byte:
		*j = append((*j)[:0], v...)
	case nil:
		*j = []byte("null")
	default:
		return fmt.Errorf("invalid registration JSON type %T", value)
	}
	return nil
}
func (j RegistrationJSON) MarshalJSON() ([]byte, error) { return json.RawMessage(j).MarshalJSON() }
func (j *RegistrationJSON) UnmarshalJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid registration JSON")
	}
	*j = append((*j)[:0], data...)
	return nil
}
