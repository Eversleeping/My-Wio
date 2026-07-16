package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/wio-platform/wio/internal/protocol"
)

//go:embed schema.sql
var schema string

type Store struct {
	DB     *sqlx.DB
	driver string
}

func Open(databaseURL string) (*Store, error) {
	driver, dsn := "sqlite", databaseURL
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		driver = "pgx"
	} else if dsn == "" {
		dsn = "wio.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	db, err := sqlx.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{DB: db, driver: driver}, nil
}

func (s *Store) Close() error      { return s.DB.Close() }
func (s *Store) Q(q string) string { return s.DB.Rebind(q) }
func NewID() string                { return uuid.NewString() }
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type User struct {
	ID             string `db:"id" json:"id"`
	Username       string `db:"username" json:"username"`
	PasswordHash   string `db:"password_hash" json:"-"`
	TOTPSecret     string `db:"totp_secret" json:"-"`
	RecoveryHashes string `db:"recovery_hashes" json:"-"`
}

func (s *Store) HasUser(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.GetContext(ctx, &n, "SELECT COUNT(*) FROM users")
	return n > 0, err
}

func (s *Store) CreateUser(ctx context.Context, u User) error {
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO users(id,username,password_hash,totp_secret,recovery_hashes) VALUES(?,?,?,?,?)"), u.ID, u.Username, u.PasswordHash, u.TOTPSecret, u.RecoveryHashes)
	return err
}

func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	var u User
	err := s.DB.GetContext(ctx, &u, s.Q("SELECT id,username,password_hash,totp_secret,recovery_hashes FROM users WHERE username=?"), name)
	return u, err
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

type Server struct {
	ID           string     `db:"id" json:"id"`
	Name         string     `db:"name" json:"name"`
	Hostname     string     `db:"hostname" json:"hostname"`
	Status       string     `db:"status" json:"status"`
	AgentVersion string     `db:"agent_version" json:"agent_version"`
	CodexVersion string     `db:"codex_version" json:"codex_version"`
	CodexReady   int        `db:"codex_ready" json:"codex_ready"`
	ScanRoots    string     `db:"scan_roots" json:"-"`
	LastSeenAt   *time.Time `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
}

func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	var out []Server
	err := s.DB.SelectContext(ctx, &out, "SELECT id,name,hostname,status,agent_version,codex_version,codex_ready,scan_roots,last_seen_at,created_at FROM servers WHERE revoked_at IS NULL ORDER BY name")
	return out, err
}

func (s *Store) CreateEnrollment(ctx context.Context, name string, roots []string, token string, expires time.Time) (string, error) {
	id := NewID()
	raw, _ := json.Marshal(roots)
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO enrollment_tokens(id,token_hash,server_name,scan_roots,expires_at) VALUES(?,?,?,?,?)"), id, HashToken(token), name, string(raw), expires)
	return id, err
}

type Enrollment struct {
	ID         string    `db:"id"`
	ServerName string    `db:"server_name"`
	ScanRoots  string    `db:"scan_roots"`
	ExpiresAt  time.Time `db:"expires_at"`
}

func (s *Store) ConsumeEnrollment(ctx context.Context, token string) (Enrollment, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback()
	var e Enrollment
	err = tx.GetContext(ctx, &e, s.Q("SELECT id,server_name,scan_roots,expires_at FROM enrollment_tokens WHERE token_hash=? AND consumed_at IS NULL AND expires_at>?"), HashToken(token), time.Now().UTC())
	if err != nil {
		return Enrollment{}, err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE enrollment_tokens SET consumed_at=? WHERE id=? AND consumed_at IS NULL"), time.Now().UTC(), e.ID); err != nil {
		return Enrollment{}, err
	}
	if err = tx.Commit(); err != nil {
		return Enrollment{}, err
	}
	return e, nil
}

func (s *Store) EnrollServer(ctx context.Context, e Enrollment, hostname, agentToken string) (Server, error) {
	server := Server{ID: NewID(), Name: e.ServerName, Hostname: hostname, Status: "offline", ScanRoots: e.ScanRoots, CreatedAt: time.Now().UTC()}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return server, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO servers(id,name,hostname,scan_roots) VALUES(?,?,?,?)"), server.ID, server.Name, hostname, e.ScanRoots)
	if err != nil {
		return server, err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO agent_credentials(server_id,token_hash) VALUES(?,?)"), server.ID, HashToken(agentToken))
	if err != nil {
		return server, err
	}
	return server, tx.Commit()
}

func (s *Store) AuthenticateAgent(ctx context.Context, token string) (string, error) {
	var id string
	err := s.DB.GetContext(ctx, &id, s.Q(`SELECT c.server_id FROM agent_credentials c JOIN servers s ON s.id=c.server_id WHERE c.token_hash=? AND c.revoked_at IS NULL AND s.revoked_at IS NULL`), HashToken(token))
	return id, err
}

func (s *Store) Heartbeat(ctx context.Context, serverID string, h protocol.Heartbeat) error {
	ready := 0
	if h.CodexReady {
		ready = 1
	}
	roots, _ := json.Marshal(h.ScanRoots)
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE servers SET hostname=?,status='online',agent_version=?,codex_version=?,codex_ready=?,scan_roots=?,last_seen_at=? WHERE id=? AND revoked_at IS NULL`), h.Hostname, h.AgentVersion, h.CodexVersion, ready, string(roots), time.Now().UTC(), serverID)
	return err
}

func normalizeRemote(raw string) string {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, ".git"))
	if strings.HasPrefix(raw, "git@") {
		raw = strings.Replace(raw, ":", "/", 1)
		raw = strings.Replace(raw, "git@", "ssh://git@", 1)
	}
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		raw = strings.TrimSuffix(u.String(), "/")
	}
	return strings.ToLower(raw)
}

func (s *Store) UpsertInventory(ctx context.Context, serverID string, inv protocol.Inventory) error {
	for _, repo := range inv.Repositories {
		normalized := normalizeRemote(repo.RemoteURL)
		var projectID string
		if normalized != "" {
			_ = s.DB.GetContext(ctx, &projectID, s.Q("SELECT id FROM projects WHERE normalized_remote=?"), normalized)
		}
		if projectID == "" {
			_ = s.DB.GetContext(ctx, &projectID, s.Q("SELECT project_id FROM workspaces WHERE server_id=? AND path=?"), serverID, repo.Path)
		}
		if projectID == "" {
			projectID = NewID()
			if _, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO projects(id,name,remote_url,normalized_remote) VALUES(?,?,?,?)"), projectID, repo.Name, repo.RemoteURL, normalized); err != nil {
				return err
			}
		}
		workspaceID := NewID()
		dirty := 0
		if repo.Dirty {
			dirty = 1
		}
		_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,branch,commit_sha,dirty,last_scanned_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(server_id,path) DO UPDATE SET project_id=excluded.project_id,branch=excluded.branch,commit_sha=excluded.commit_sha,dirty=excluded.dirty,last_scanned_at=excluded.last_scanned_at`), workspaceID, projectID, serverID, repo.Path, repo.Branch, repo.CommitSHA, dirty, time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return nil
}

type Project struct {
	ID             string    `db:"id" json:"id"`
	Name           string    `db:"name" json:"name"`
	RemoteURL      string    `db:"remote_url" json:"remote_url"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
	WorkspaceCount int       `db:"workspace_count" json:"workspace_count"`
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	err := s.DB.SelectContext(ctx, &out, `SELECT p.id,p.name,p.remote_url,p.updated_at,COUNT(w.id) workspace_count FROM projects p LEFT JOIN workspaces w ON w.project_id=p.id GROUP BY p.id,p.name,p.remote_url,p.updated_at ORDER BY p.name`)
	return out, err
}

type Workspace struct {
	ID          string `db:"id" json:"id"`
	ProjectID   string `db:"project_id" json:"project_id"`
	ServerID    string `db:"server_id" json:"server_id"`
	Path        string `db:"path" json:"path"`
	Branch      string `db:"branch" json:"branch"`
	CommitSHA   string `db:"commit_sha" json:"commit_sha"`
	Dirty       int    `db:"dirty" json:"dirty"`
	ServerName  string `db:"server_name" json:"server_name"`
	ProjectName string `db:"project_name" json:"project_name"`
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	var out []Workspace
	err := s.DB.SelectContext(ctx, &out, `SELECT w.id,w.project_id,w.server_id,w.path,w.branch,w.commit_sha,w.dirty,s.name server_name,p.name project_name FROM workspaces w JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id ORDER BY p.name,s.name`)
	return out, err
}

func (s *Store) QueueOperation(ctx context.Context, serverID, kind string, payload any, idempotency string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.queueOperationPayload(ctx, serverID, kind, string(raw), idempotency)
}

func (s *Store) QueueEncryptedOperation(ctx context.Context, serverID, kind, ciphertext, idempotency string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted operation payload must use a supported Vault format")
	}
	return s.queueOperationPayload(ctx, serverID, kind, ciphertext, idempotency)
}

func (s *Store) queueOperationPayload(ctx context.Context, serverID, kind, payload, idempotency string) (string, error) {
	id := NewID()
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key) VALUES(?,?,?,?,?) ON CONFLICT(idempotency_key) DO NOTHING"), id, serverID, kind, payload, idempotency)
	if err != nil {
		return "", err
	}
	var existing string
	if err := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); err != nil {
		return "", err
	}
	return existing, nil
}

type Operation struct {
	ID        string    `db:"id"`
	ServerID  string    `db:"server_id"`
	Kind      string    `db:"kind"`
	Payload   string    `db:"payload"`
	CreatedAt time.Time `db:"created_at"`
}

func (s *Store) PendingOperations(ctx context.Context, serverID string) ([]Operation, error) {
	var out []Operation
	err := s.DB.SelectContext(ctx, &out, s.Q("SELECT id,server_id,kind,payload,created_at FROM agent_operations WHERE server_id=? AND (status='queued' OR (status='delivered' AND delivered_at<?)) ORDER BY created_at LIMIT 100"), serverID, time.Now().UTC().Add(-30*time.Second))
	return out, err
}
func (s *Store) MarkDelivered(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='delivered',delivered_at=? WHERE id=? AND status='queued'"), time.Now().UTC(), id)
	return err
}
func (s *Store) CompleteOperation(ctx context.Context, r protocol.OperationResult) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=?,result=?,completed_at=? WHERE id=?"), r.Status, r.Message, time.Now().UTC(), r.OperationID)
	return err
}

func (s *Store) AddEvent(ctx context.Context, event protocol.StreamEvent) (protocol.StreamEvent, error) {
	if event.EventID == "" {
		event.EventID = NewID()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	if event.Sequence == 0 {
		if err := tx.GetContext(ctx, &event.Sequence, s.Q("SELECT COALESCE(MAX(sequence),0)+1 FROM events WHERE stream_id=?"), event.StreamID); err != nil {
			return event, err
		}
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(stream_id,sequence) DO NOTHING"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(event.Payload))
	if err != nil {
		return event, err
	}
	if err := tx.Commit(); err != nil {
		return event, err
	}
	return event, nil
}

func (s *Store) Events(ctx context.Context, streamID string, after int64, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	var rows []struct {
		EventID, StreamID, Kind, Payload string
		Sequence                         int64
		OccurredAt                       time.Time
	}
	err := s.DB.SelectContext(ctx, &rows, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE (?='' OR stream_id=?) AND sequence>? ORDER BY occurred_at,sequence LIMIT ?"), streamID, streamID, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.StreamEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, protocol.StreamEvent{EventID: r.EventID, StreamID: r.StreamID, Sequence: r.Sequence, Kind: r.Kind, OccurredAt: r.OccurredAt, Payload: json.RawMessage(r.Payload)})
	}
	return out, nil
}

func (s *Store) SaveMetrics(ctx context.Context, serverID string, m protocol.Metrics) error {
	now := time.Now().UTC().Truncate(time.Minute)
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO metric_rollups(server_id,bucket_at,resolution,cpu_percent,memory_percent,disk_percent,load_1,net_rx_bytes,net_tx_bytes,samples) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(server_id,bucket_at,resolution) DO UPDATE SET cpu_percent=(metric_rollups.cpu_percent*metric_rollups.samples+excluded.cpu_percent)/(metric_rollups.samples+1),memory_percent=(metric_rollups.memory_percent*metric_rollups.samples+excluded.memory_percent)/(metric_rollups.samples+1),disk_percent=(metric_rollups.disk_percent*metric_rollups.samples+excluded.disk_percent)/(metric_rollups.samples+1),load_1=(metric_rollups.load_1*metric_rollups.samples+excluded.load_1)/(metric_rollups.samples+1),net_rx_bytes=excluded.net_rx_bytes,net_tx_bytes=excluded.net_tx_bytes,samples=metric_rollups.samples+1`), serverID, now, "minute", m.CPUPercent, m.MemoryPercent, m.DiskPercent, m.Load1, m.NetRxBytes, m.NetTxBytes)
	return err
}

func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
