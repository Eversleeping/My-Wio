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
	"path"
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

var ErrWorkspaceWriteActive = errors.New("workspace already has an active write operation")

type Store struct {
	DB     *sqlx.DB
	driver string
}

const ServerOnlineGracePeriod = 90 * time.Second

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
		_ = db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := migrateUserAuthMode(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateImportedProjectRemotes(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateCredentialProfileIdentity(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateProjectThreadPreferences(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateWorkspaceMetadata(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateProjectWorkspaceOperations(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateManagedWorkspacePaths(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateDeploymentSources(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateDeploymentPublicURL(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateRepairEnrollments(ctx, db, driver); err != nil {
		_ = db.Close()
		return nil, err
	}
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, `ALTER TABLE metric_rollups
			ALTER COLUMN net_rx_bytes TYPE BIGINT USING net_rx_bytes::BIGINT,
			ALTER COLUMN net_tx_bytes TYPE BIGINT USING net_tx_bytes::BIGINT`); err != nil {
			return nil, fmt.Errorf("migrate metric counters: %w", err)
		}
	}
	return &Store{DB: db, driver: driver}, nil
}

func migrateUserAuthMode(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, "ALTER TABLE users ADD COLUMN IF NOT EXISTS auth_mode TEXT NOT NULL DEFAULT 'totp'"); err != nil {
			return fmt.Errorf("migrate user authentication mode: %w", err)
		}
		return nil
	}
	var count int
	if err := db.GetContext(ctx, &count, "SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='auth_mode'"); err != nil {
		return fmt.Errorf("inspect user authentication mode: %w", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE users ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'totp'"); err != nil {
			return fmt.Errorf("migrate user authentication mode: %w", err)
		}
	}
	return nil
}

func migrateRepairEnrollments(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, "ALTER TABLE enrollment_tokens ADD COLUMN IF NOT EXISTS server_id TEXT REFERENCES servers(id) ON DELETE CASCADE"); err != nil {
			return fmt.Errorf("migrate repair enrollments: %w", err)
		}
		return nil
	}
	var count int
	if err := db.GetContext(ctx, &count, "SELECT COUNT(*) FROM pragma_table_info('enrollment_tokens') WHERE name='server_id'"); err != nil {
		return fmt.Errorf("inspect repair enrollment column: %w", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE enrollment_tokens ADD COLUMN server_id TEXT REFERENCES servers(id) ON DELETE CASCADE"); err != nil {
			return fmt.Errorf("migrate repair enrollment column: %w", err)
		}
	}
	return nil
}

func migrateDeploymentSources(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		_, err := db.ExecContext(ctx, `ALTER TABLE deployment_targets
			ADD COLUMN IF NOT EXISTS source_type TEXT NOT NULL DEFAULT 'remote',
			ADD COLUMN IF NOT EXISTS workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL`)
		if err != nil {
			return fmt.Errorf("migrate deployment sources: %w", err)
		}
		return nil
	}
	var columns []string
	if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('deployment_targets')"); err != nil {
		return fmt.Errorf("inspect deployment target columns: %w", err)
	}
	existing := make(map[string]bool, len(columns))
	for _, column := range columns {
		existing[column] = true
	}
	for column, definition := range map[string]string{
		"source_type":  "TEXT NOT NULL DEFAULT 'remote'",
		"workspace_id": "TEXT REFERENCES workspaces(id) ON DELETE SET NULL",
	} {
		if !existing[column] {
			if _, err := db.ExecContext(ctx, "ALTER TABLE deployment_targets ADD COLUMN "+column+" "+definition); err != nil {
				return fmt.Errorf("migrate deployment target column %s: %w", column, err)
			}
		}
	}
	return nil
}

func migrateDeploymentPublicURL(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, "ALTER TABLE deployment_targets ADD COLUMN IF NOT EXISTS public_url TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate deployment public URL: %w", err)
		}
		return nil
	}
	var count int
	if err := db.GetContext(ctx, &count, "SELECT COUNT(*) FROM pragma_table_info('deployment_targets') WHERE name='public_url'"); err != nil {
		return fmt.Errorf("inspect deployment public URL column: %w", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE deployment_targets ADD COLUMN public_url TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate deployment public URL column: %w", err)
		}
	}
	return nil
}

func migrateProjectWorkspaceOperations(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		statements := []string{
			`ALTER TABLE projects
				ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS default_branch TEXT NOT NULL DEFAULT 'main',
				ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ready',
				ADD COLUMN IF NOT EXISTS provision_error TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS archived_at TIMESTAMP`,
			`ALTER TABLE workspaces
				ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
				ADD COLUMN IF NOT EXISTS management_mode TEXT NOT NULL DEFAULT 'observed',
				ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ready',
				ADD COLUMN IF NOT EXISTS last_git_refresh_at TIMESTAMP,
				ADD COLUMN IF NOT EXISTS git_error TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE agent_operations
				ADD COLUMN IF NOT EXISTS project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
				ADD COLUMN IF NOT EXISTS workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
				ADD COLUMN IF NOT EXISTS workspace_write INTEGER NOT NULL DEFAULT 0,
				ADD COLUMN IF NOT EXISTS result_data TEXT NOT NULL DEFAULT '{}'`,
			`ALTER TABLE servers ADD COLUMN IF NOT EXISTS managed_roots TEXT NOT NULL DEFAULT '[]'`,
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate project workspace operations: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, "UPDATE agent_operations SET result_data='{}' WHERE result_data IS NULL"); err != nil {
			return fmt.Errorf("normalize operation result data: %w", err)
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE agent_operations
			ALTER COLUMN result_data SET DEFAULT '{}',
			ALTER COLUMN result_data SET NOT NULL`); err != nil {
			return fmt.Errorf("constrain operation result data: %w", err)
		}
	} else {
		tables := []struct {
			name    string
			columns map[string]string
		}{
			{name: "projects", columns: map[string]string{
				"description":     "TEXT NOT NULL DEFAULT ''",
				"default_branch":  "TEXT NOT NULL DEFAULT 'main'",
				"status":          "TEXT NOT NULL DEFAULT 'ready'",
				"provision_error": "TEXT NOT NULL DEFAULT ''",
				"archived_at":     "TIMESTAMP",
			}},
			{name: "workspaces", columns: map[string]string{
				"display_name":        "TEXT NOT NULL DEFAULT ''",
				"management_mode":     "TEXT NOT NULL DEFAULT 'observed'",
				"status":              "TEXT NOT NULL DEFAULT 'ready'",
				"last_git_refresh_at": "TIMESTAMP",
				"git_error":           "TEXT NOT NULL DEFAULT ''",
			}},
			{name: "agent_operations", columns: map[string]string{
				"project_id":      "TEXT REFERENCES projects(id) ON DELETE SET NULL",
				"workspace_id":    "TEXT REFERENCES workspaces(id) ON DELETE SET NULL",
				"workspace_write": "INTEGER NOT NULL DEFAULT 0",
				"result_data":     "TEXT NOT NULL DEFAULT '{}'",
			}},
			{name: "servers", columns: map[string]string{
				"managed_roots": "TEXT NOT NULL DEFAULT '[]'",
			}},
		}
		for _, table := range tables {
			var columns []string
			if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info(?)", table.name); err != nil {
				return fmt.Errorf("inspect %s resource columns: %w", table.name, err)
			}
			existing := make(map[string]bool, len(columns))
			for _, column := range columns {
				existing[column] = true
			}
			for column, definition := range table.columns {
				if existing[column] {
					continue
				}
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+table.name+" ADD COLUMN "+column+" "+definition); err != nil {
					return fmt.Errorf("migrate %s resource column %s: %w", table.name, column, err)
				}
			}
		}
		if _, err := db.ExecContext(ctx, "UPDATE agent_operations SET result_data='{}' WHERE result_data IS NULL"); err != nil {
			return fmt.Errorf("normalize operation result data: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET status=CASE status
		WHEN 'moveing' THEN 'moving'
		WHEN 'deleteing' THEN 'deleting'
		ELSE status END
		WHERE status IN ('moveing','deleteing')`); err != nil {
		return fmt.Errorf("normalize workspace lifecycle status: %w", err)
	}
	// Worktrees created before management_mode was introduced were assigned the
	// observed default during migration. Their ownership link is durable proof
	// that Wio created them, so restore the managed mode expected by Git writes
	// and physical workspace deletion.
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET management_mode='managed'
		WHERE management_mode='observed' AND kind='worktree' AND parent_workspace_id IS NOT NULL`); err != nil {
		return fmt.Errorf("restore managed worktree ownership: %w", err)
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS operations_project_idx ON agent_operations(project_id, created_at)",
		"CREATE INDEX IF NOT EXISTS operations_workspace_idx ON agent_operations(workspace_id, created_at)",
		"CREATE UNIQUE INDEX IF NOT EXISTS operations_workspace_active_write_unique ON agent_operations(workspace_id) WHERE workspace_id IS NOT NULL AND workspace_write=1 AND status IN ('queued','delivered','running')",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create resource operation index: %w", err)
		}
	}
	return nil
}

func migrateManagedWorkspacePaths(ctx context.Context, db *sqlx.DB) error {
	var servers []struct {
		ID           string `db:"id"`
		ManagedRoots string `db:"managed_roots"`
	}
	if err := db.SelectContext(ctx, &servers, "SELECT id,managed_roots FROM servers"); err != nil {
		return fmt.Errorf("load managed workspace roots: %w", err)
	}
	for _, server := range servers {
		roots := decodeManagedRoots(server.ManagedRoots)
		if len(roots) == 0 {
			continue
		}
		var workspaces []struct {
			ID   string `db:"id"`
			Path string `db:"path"`
		}
		if err := db.SelectContext(ctx, &workspaces, db.Rebind("SELECT id,path FROM workspaces WHERE server_id=? AND management_mode='observed'"), server.ID); err != nil {
			return fmt.Errorf("load observed workspaces: %w", err)
		}
		for _, workspace := range workspaces {
			if !insideManagedWorkspaceRoot(workspace.Path, roots) {
				continue
			}
			if _, err := db.ExecContext(ctx, db.Rebind("UPDATE workspaces SET management_mode='managed' WHERE id=? AND management_mode='observed'"), workspace.ID); err != nil {
				return fmt.Errorf("restore managed workspace path: %w", err)
			}
		}
	}
	return nil
}

func decodeManagedRoots(raw string) []string {
	var roots []string
	_ = json.Unmarshal([]byte(raw), &roots)
	return roots
}

func insideManagedWorkspaceRoot(workspacePath string, roots []string) bool {
	workspacePath = path.Clean(strings.TrimSpace(workspacePath))
	if workspacePath == "." || !strings.HasPrefix(workspacePath, "/") {
		return false
	}
	for _, root := range roots {
		root = path.Clean(strings.TrimSpace(root))
		if root == "." || !strings.HasPrefix(root, "/") || workspacePath == root {
			continue
		}
		prefix := strings.TrimSuffix(root, "/") + "/"
		if strings.HasPrefix(workspacePath, prefix) {
			return true
		}
	}
	return false
}

func migrateWorkspaceMetadata(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		_, err := db.ExecContext(ctx, `ALTER TABLE workspaces
			ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'primary',
			ADD COLUMN IF NOT EXISTS parent_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL`)
		if err != nil {
			return fmt.Errorf("migrate workspace metadata: %w", err)
		}
		return nil
	}
	var columns []string
	if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('workspaces')"); err != nil {
		return fmt.Errorf("inspect workspace metadata columns: %w", err)
	}
	existing := make(map[string]bool, len(columns))
	for _, column := range columns {
		existing[column] = true
	}
	if !existing["kind"] {
		if _, err := db.ExecContext(ctx, "ALTER TABLE workspaces ADD COLUMN kind TEXT NOT NULL DEFAULT 'primary'"); err != nil {
			return fmt.Errorf("migrate workspace kind: %w", err)
		}
	}
	if !existing["parent_workspace_id"] {
		if _, err := db.ExecContext(ctx, "ALTER TABLE workspaces ADD COLUMN parent_workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL"); err != nil {
			return fmt.Errorf("migrate workspace parent: %w", err)
		}
	}
	return nil
}

func migrateProjectThreadPreferences(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, `ALTER TABLE projects
			ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMP,
			ADD COLUMN IF NOT EXISTS hidden_at TIMESTAMP`); err != nil {
			return fmt.Errorf("migrate project preferences: %w", err)
		}
		if _, err := db.ExecContext(ctx, `ALTER TABLE codex_threads
			ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMP,
			ADD COLUMN IF NOT EXISTS archived_at TIMESTAMP`); err != nil {
			return fmt.Errorf("migrate thread preferences: %w", err)
		}
		return nil
	}
	for _, table := range []struct {
		name    string
		columns []string
	}{
		{name: "projects", columns: []string{"pinned_at", "hidden_at"}},
		{name: "codex_threads", columns: []string{"pinned_at", "archived_at"}},
	} {
		var columns []string
		if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info(?)", table.name); err != nil {
			return fmt.Errorf("inspect %s preference columns: %w", table.name, err)
		}
		existing := make(map[string]bool, len(columns))
		for _, column := range columns {
			existing[column] = true
		}
		for _, column := range table.columns {
			if existing[column] {
				continue
			}
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+table.name+" ADD COLUMN "+column+" TIMESTAMP"); err != nil {
				return fmt.Errorf("migrate %s preference column %s: %w", table.name, column, err)
			}
		}
	}
	return nil
}

func migrateCredentialProfileIdentity(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, `ALTER TABLE credential_profiles
			ADD COLUMN IF NOT EXISTS commit_name TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS commit_email TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate Git commit identity: %w", err)
		}
		return nil
	}
	var columns []string
	if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('credential_profiles')"); err != nil {
		return fmt.Errorf("inspect Git commit identity columns: %w", err)
	}
	existing := make(map[string]bool, len(columns))
	for _, column := range columns {
		existing[column] = true
	}
	for _, column := range []string{"commit_name", "commit_email"} {
		if existing[column] {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE credential_profiles ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("migrate Git commit identity column %s: %w", column, err)
		}
	}
	return nil
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

type Server struct {
	ID                   string     `db:"id" json:"id"`
	Name                 string     `db:"name" json:"name"`
	Hostname             string     `db:"hostname" json:"hostname"`
	Status               string     `db:"status" json:"status"`
	AgentVersion         string     `db:"agent_version" json:"agent_version"`
	CodexVersion         string     `db:"codex_version" json:"codex_version"`
	CodexReady           int        `db:"codex_ready" json:"codex_ready"`
	ScanRoots            string     `db:"scan_roots" json:"-"`
	ManagedRoots         string     `db:"managed_roots" json:"-"`
	Address              string     `db:"address" json:"address"`
	Configuration        string     `db:"configuration" json:"configuration"`
	Notes                string     `db:"notes" json:"notes"`
	LastSeenAt           *time.Time `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt            time.Time  `db:"created_at" json:"created_at"`
	AgentTargetVersion   string     `db:"-" json:"agent_target_version"`
	AgentUpdateAvailable bool       `db:"-" json:"agent_update_available"`
	AgentUpdateSupported bool       `db:"-" json:"agent_update_supported"`
	CodexTargetVersion   string     `db:"-" json:"codex_target_version"`
	CodexUpdateAvailable bool       `db:"-" json:"codex_update_available"`
	CodexUpdateSupported bool       `db:"-" json:"codex_update_supported"`
	CodexProfileID       string     `db:"codex_profile_id" json:"codex_profile_id"`
	CodexProfileName     string     `db:"codex_profile_name" json:"codex_profile_name"`
	GitProfileID         string     `db:"git_profile_id" json:"git_profile_id"`
	GitProfileName       string     `db:"git_profile_name" json:"git_profile_name"`
}

type ServerMetadata struct {
	Address       string `db:"address" json:"address"`
	Configuration string `db:"configuration" json:"configuration"`
	Notes         string `db:"notes" json:"notes"`
}

func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	var out []Server
	err := s.DB.SelectContext(ctx, &out, s.Q(`SELECT s.id,s.name,s.hostname,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END status,s.agent_version,s.codex_version,s.codex_ready,s.scan_roots,s.managed_roots,COALESCE(m.address,'') address,COALESCE(m.configuration,'') configuration,COALESCE(m.notes,'') notes,s.last_seen_at,s.created_at,COALESCE(cp.codex_profile_id,'') codex_profile_id,COALESCE(codex.name,'') codex_profile_name,COALESCE(cp.git_profile_id,'') git_profile_id,COALESCE(git.name,'') git_profile_name FROM servers s LEFT JOIN server_metadata m ON m.server_id=s.id LEFT JOIN server_credential_profiles cp ON cp.server_id=s.id LEFT JOIN credential_profiles codex ON codex.id=cp.codex_profile_id LEFT JOIN credential_profiles git ON git.id=cp.git_profile_id WHERE s.revoked_at IS NULL ORDER BY s.name`), time.Now().UTC().Add(-ServerOnlineGracePeriod))
	return out, err
}

func (s *Store) Server(ctx context.Context, id string) (Server, error) {
	var server Server
	err := s.DB.GetContext(ctx, &server, s.Q(`SELECT s.id,s.name,s.hostname,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END status,s.agent_version,s.codex_version,s.codex_ready,s.scan_roots,s.managed_roots,COALESCE(m.address,'') address,COALESCE(m.configuration,'') configuration,COALESCE(m.notes,'') notes,s.last_seen_at,s.created_at,COALESCE(cp.codex_profile_id,'') codex_profile_id,COALESCE(codex.name,'') codex_profile_name,COALESCE(cp.git_profile_id,'') git_profile_id,COALESCE(git.name,'') git_profile_name FROM servers s LEFT JOIN server_metadata m ON m.server_id=s.id LEFT JOIN server_credential_profiles cp ON cp.server_id=s.id LEFT JOIN credential_profiles codex ON codex.id=cp.codex_profile_id LEFT JOIN credential_profiles git ON git.id=cp.git_profile_id WHERE s.id=? AND s.revoked_at IS NULL`), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	return server, err
}

func (s *Store) CreateEnrollment(ctx context.Context, name string, roots []string, token string, expires time.Time) (string, error) {
	return s.CreateEnrollmentWithMetadata(ctx, name, roots, token, expires, ServerMetadata{})
}

func (s *Store) CreateEnrollmentWithMetadata(ctx context.Context, name string, roots []string, token string, expires time.Time, metadata ServerMetadata) (string, error) {
	return s.createEnrollment(ctx, "", name, roots, token, expires, metadata)
}

func (s *Store) CreateRepairEnrollment(ctx context.Context, serverID, token string, expires time.Time) (string, error) {
	server, err := s.Server(ctx, serverID)
	if err != nil {
		return "", err
	}
	var roots []string
	if err := json.Unmarshal([]byte(server.ScanRoots), &roots); err != nil {
		return "", err
	}
	return s.createEnrollment(ctx, serverID, server.Name, roots, token, expires, ServerMetadata{Address: server.Address, Configuration: server.Configuration, Notes: server.Notes})
}

func (s *Store) createEnrollment(ctx context.Context, serverID, name string, roots []string, token string, expires time.Time, metadata ServerMetadata) (string, error) {
	id := NewID()
	raw, _ := json.Marshal(roots)
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return id, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO enrollment_tokens(id,token_hash,server_id,server_name,scan_roots,expires_at) VALUES(?,?,NULLIF(?,''),?,?,?)"), id, HashToken(token), serverID, name, string(raw), expires); err != nil {
		return id, err
	}
	if _, err = tx.ExecContext(ctx, s.Q("INSERT INTO enrollment_metadata(enrollment_id,address,configuration,notes) VALUES(?,?,?,?)"), id, metadata.Address, metadata.Configuration, metadata.Notes); err != nil {
		return id, err
	}
	return id, tx.Commit()
}

func (s *Store) DeleteUnusedEnrollment(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("DELETE FROM enrollment_tokens WHERE id=? AND consumed_at IS NULL"), id)
	return err
}

type Enrollment struct {
	ID            string    `db:"id"`
	ServerID      string    `db:"server_id"`
	ServerName    string    `db:"server_name"`
	ScanRoots     string    `db:"scan_roots"`
	Address       string    `db:"address"`
	Configuration string    `db:"configuration"`
	Notes         string    `db:"notes"`
	ExpiresAt     time.Time `db:"expires_at"`
}

func (s *Store) ConsumeEnrollment(ctx context.Context, token string) (Enrollment, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback()
	var e Enrollment
	err = tx.GetContext(ctx, &e, s.Q(`SELECT e.id,COALESCE(e.server_id,'') server_id,e.server_name,e.scan_roots,COALESCE(m.address,'') address,COALESCE(m.configuration,'') configuration,COALESCE(m.notes,'') notes,e.expires_at FROM enrollment_tokens e LEFT JOIN enrollment_metadata m ON m.enrollment_id=e.id WHERE e.token_hash=? AND e.consumed_at IS NULL AND e.expires_at>?`), HashToken(token), time.Now().UTC())
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
	if e.ServerID != "" {
		return s.repairServerEnrollment(ctx, e, hostname, agentToken)
	}
	server := Server{ID: NewID(), Name: e.ServerName, Hostname: hostname, Status: "offline", ScanRoots: e.ScanRoots, Address: e.Address, Configuration: e.Configuration, Notes: e.Notes, CreatedAt: time.Now().UTC()}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return server, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO servers(id,name,hostname,scan_roots) VALUES(?,?,?,?)"), server.ID, server.Name, hostname, e.ScanRoots)
	if err != nil {
		return server, err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO server_metadata(server_id,address,configuration,notes) VALUES(?,?,?,?)"), server.ID, e.Address, e.Configuration, e.Notes)
	if err != nil {
		return server, err
	}
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO agent_credentials(server_id,token_hash) VALUES(?,?)"), server.ID, HashToken(agentToken))
	if err != nil {
		return server, err
	}
	return server, tx.Commit()
}

func (s *Store) repairServerEnrollment(ctx context.Context, e Enrollment, hostname, agentToken string) (Server, error) {
	server := Server{ID: e.ServerID, Name: e.ServerName, Hostname: hostname, Status: "offline", ScanRoots: e.ScanRoots, Address: e.Address, Configuration: e.Configuration, Notes: e.Notes}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return server, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET hostname=?,scan_roots=?,status='offline',agent_version='',codex_version='',codex_ready=0,last_seen_at=NULL WHERE id=? AND revoked_at IS NULL"), hostname, e.ScanRoots, e.ServerID)
	if err != nil {
		return server, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		if err == nil {
			err = sql.ErrNoRows
		}
		return server, err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE agent_credentials SET token_hash=?,created_at=?,revoked_at=NULL WHERE server_id=?"), HashToken(agentToken), time.Now().UTC(), e.ServerID); err != nil {
		return server, err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_metadata(server_id,address,configuration,notes,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET address=excluded.address,configuration=excluded.configuration,notes=excluded.notes,updated_at=excluded.updated_at`), e.ServerID, e.Address, e.Configuration, e.Notes, time.Now().UTC()); err != nil {
		return server, err
	}
	return server, tx.Commit()
}

func (s *Store) UpdateServerMetadata(ctx context.Context, serverID string, metadata ServerMetadata) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO server_metadata(server_id,address,configuration,notes,updated_at) SELECT id,?,?,?,? FROM servers WHERE id=? AND revoked_at IS NULL ON CONFLICT(server_id) DO UPDATE SET address=excluded.address,configuration=excluded.configuration,notes=excluded.notes,updated_at=excluded.updated_at`), metadata.Address, metadata.Configuration, metadata.Notes, time.Now().UTC(), serverID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
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
	managedRoots, _ := json.Marshal(h.ManagedRoots)
	_, err := s.DB.ExecContext(ctx, s.Q(`UPDATE servers SET hostname=?,status='online',agent_version=?,codex_version=?,codex_ready=?,scan_roots=?,managed_roots=?,last_seen_at=? WHERE id=? AND revoked_at IS NULL`), h.Hostname, h.AgentVersion, h.CodexVersion, ready, string(roots), string(managedRoots), time.Now().UTC(), serverID)
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

func migrateImportedProjectRemotes(ctx context.Context, db *sqlx.DB) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO project_remotes(id,project_id,name,mode,fetch_url,push_url,status,created_at,updated_at)
		SELECT 'import-origin-' || p.id,p.id,'origin','existing',p.remote_url,p.remote_url,'ready',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		FROM projects p
		WHERE TRIM(p.remote_url) <> ''
		ON CONFLICT(project_id,name) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("migrate imported project remotes: %w", err)
	}
	return nil
}

func (s *Store) UpsertInventory(ctx context.Context, serverID string, inv protocol.Inventory) error {
	var managedRootsJSON string
	if err := s.DB.GetContext(ctx, &managedRootsJSON, s.Q("SELECT managed_roots FROM servers WHERE id=?"), serverID); err != nil {
		return err
	}
	managedRoots := decodeManagedRoots(managedRootsJSON)
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
		if err := s.ensureProjectRemote(ctx, s.DB, projectID, repo.RemoteURL); err != nil {
			return err
		}
		workspaceID := NewID()
		dirty := 0
		if repo.Dirty {
			dirty = 1
		}
		managementMode := "observed"
		if insideManagedWorkspaceRoot(repo.Path, managedRoots) {
			managementMode = "managed"
		}
		now := time.Now().UTC()
		_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id,path) DO UPDATE SET project_id=excluded.project_id,management_mode=CASE WHEN excluded.management_mode='managed' THEN 'managed' ELSE workspaces.management_mode END,branch=excluded.branch,commit_sha=excluded.commit_sha,dirty=excluded.dirty,status='ready',last_git_refresh_at=excluded.last_git_refresh_at,git_error='',last_scanned_at=excluded.last_scanned_at`), workspaceID, projectID, serverID, repo.Path, repo.Name, managementMode, repo.Branch, repo.CommitSHA, dirty, now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

type Project struct {
	ID                string     `db:"id" json:"id"`
	Name              string     `db:"name" json:"name"`
	Description       string     `db:"description" json:"description"`
	RemoteURL         string     `db:"remote_url" json:"remote_url"`
	DefaultBranch     string     `db:"default_branch" json:"default_branch"`
	Status            string     `db:"status" json:"status"`
	ProvisionError    string     `db:"provision_error" json:"provision_error"`
	PinnedAt          *time.Time `db:"pinned_at" json:"pinned_at"`
	HiddenAt          *time.Time `db:"hidden_at" json:"hidden_at"`
	ArchivedAt        *time.Time `db:"archived_at" json:"archived_at"`
	UpdatedAt         time.Time  `db:"updated_at" json:"updated_at"`
	WorkspaceCount    int        `db:"workspace_count" json:"workspace_count"`
	ImportStatus      string     `db:"-" json:"import_status"`
	ImportMessage     string     `db:"-" json:"import_message"`
	ImportServerID    string     `db:"-" json:"import_server_id"`
	ImportServerName  string     `db:"-" json:"import_server_name"`
	ImportOperationID string     `db:"-" json:"import_operation_id"`
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	var out []Project
	if err := s.DB.SelectContext(ctx, &out, `SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,(SELECT COUNT(*) FROM workspaces w WHERE w.project_id=p.id) workspace_count FROM projects p ORDER BY CASE WHEN p.pinned_at IS NULL THEN 1 ELSE 0 END,p.pinned_at DESC,p.name`); err != nil {
		return nil, err
	}
	imports, err := s.listProjectImports(ctx)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]ProjectImportOperation, len(imports))
	for _, operation := range imports {
		if _, exists := latest[operation.Command.ProjectID]; !exists {
			latest[operation.Command.ProjectID] = operation
		}
	}
	for i := range out {
		if operation, ok := latest[out[i].ID]; ok {
			out[i].ImportStatus = operation.Status
			out[i].ImportMessage = operation.Message
			out[i].ImportServerID = operation.ServerID
			out[i].ImportServerName = operation.ServerName
			out[i].ImportOperationID = operation.ID
		}
	}
	return out, nil
}

func (s *Store) Project(ctx context.Context, id string) (Project, error) {
	var project Project
	err := s.DB.GetContext(ctx, &project, s.Q(`SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,(SELECT COUNT(*) FROM workspaces w WHERE w.project_id=p.id) workspace_count FROM projects p WHERE p.id=?`), id)
	return project, err
}

func (s *Store) UpdateProject(ctx context.Context, id string, name *string, pinned, hidden *bool) (Project, error) {
	return s.UpdateProjectDetails(ctx, id, name, nil, nil, pinned, hidden, nil)
}

func (s *Store) UpdateProjectDetails(ctx context.Context, id string, name, description, defaultBranch *string, pinned, hidden, archived *bool) (Project, error) {
	sets := make([]string, 0, 4)
	args := make([]any, 0, 8)
	if name != nil {
		sets = append(sets, "name=?")
		args = append(args, *name)
	}
	if description != nil {
		sets = append(sets, "description=?")
		args = append(args, *description)
	}
	if defaultBranch != nil {
		sets = append(sets, "default_branch=?")
		args = append(args, *defaultBranch)
	}
	now := time.Now().UTC()
	if pinned != nil {
		sets = append(sets, "pinned_at=?")
		if *pinned {
			args = append(args, now)
		} else {
			args = append(args, nil)
		}
	}
	if hidden != nil {
		sets = append(sets, "hidden_at=?")
		if *hidden {
			args = append(args, now)
		} else {
			args = append(args, nil)
		}
	}
	if archived != nil {
		sets = append(sets, "archived_at=?")
		if *archived {
			args = append(args, now)
			sets = append(sets, "status='archived'")
		} else {
			args = append(args, nil)
			sets = append(sets, "status=CASE WHEN status='archived' THEN 'ready' ELSE status END")
		}
	}
	if len(sets) == 0 {
		return s.Project(ctx, id)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, now, id)
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE projects SET "+strings.Join(sets, ",")+" WHERE id=?"), args...)
	if err != nil {
		return Project{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Project{}, err
	}
	if rows == 0 {
		return Project{}, sql.ErrNoRows
	}
	return s.Project(ctx, id)
}

type ProjectImportOperation struct {
	ID         string
	ServerID   string
	ServerName string
	Status     string
	Message    string
	CreatedAt  time.Time
	Command    protocol.GitImportCommand
}

func (s *Store) listProjectImports(ctx context.Context) ([]ProjectImportOperation, error) {
	var rows []struct {
		ID         string    `db:"id"`
		ServerID   string    `db:"server_id"`
		ServerName string    `db:"server_name"`
		Status     string    `db:"status"`
		Message    string    `db:"message"`
		Payload    string    `db:"payload"`
		CreatedAt  time.Time `db:"created_at"`
	}
	if err := s.DB.SelectContext(ctx, &rows, `SELECT o.id,o.server_id,s.name server_name,o.status,COALESCE(o.result,'') message,o.payload,o.created_at FROM agent_operations o JOIN servers s ON s.id=o.server_id WHERE o.kind='git.import' ORDER BY o.created_at DESC`); err != nil {
		return nil, err
	}
	out := make([]ProjectImportOperation, 0, len(rows))
	for _, row := range rows {
		var command protocol.GitImportCommand
		if json.Unmarshal([]byte(row.Payload), &command) != nil || command.ProjectID == "" {
			continue
		}
		out = append(out, ProjectImportOperation{ID: row.ID, ServerID: row.ServerID, ServerName: row.ServerName, Status: row.Status, Message: row.Message, CreatedAt: row.CreatedAt, Command: command})
	}
	return out, nil
}

func (s *Store) LatestProjectImport(ctx context.Context, projectID string) (ProjectImportOperation, error) {
	operations, err := s.listProjectImports(ctx)
	if err != nil {
		return ProjectImportOperation{}, err
	}
	for _, operation := range operations {
		if operation.Command.ProjectID == projectID {
			return operation, nil
		}
	}
	return ProjectImportOperation{}, sql.ErrNoRows
}

func (s *Store) HasActiveProjectImport(ctx context.Context, projectID string) (bool, error) {
	operations, err := s.listProjectImports(ctx)
	if err != nil {
		return false, err
	}
	for _, operation := range operations {
		if operation.Command.ProjectID == projectID && (operation.Status == "queued" || operation.Status == "delivered") {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) DeleteProject(ctx context.Context, projectID string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.Q(`DELETE FROM projects WHERE id=? AND NOT EXISTS (SELECT 1 FROM workspaces WHERE project_id=projects.id) AND NOT EXISTS (SELECT 1 FROM deployment_targets WHERE project_id=projects.id)`), projectID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

type Workspace struct {
	ID                string     `db:"id" json:"id"`
	ProjectID         string     `db:"project_id" json:"project_id"`
	ServerID          string     `db:"server_id" json:"server_id"`
	Path              string     `db:"path" json:"path"`
	DisplayName       string     `db:"display_name" json:"display_name"`
	ManagementMode    string     `db:"management_mode" json:"management_mode"`
	Status            string     `db:"status" json:"status"`
	Kind              string     `db:"kind" json:"kind"`
	ParentWorkspaceID *string    `db:"parent_workspace_id" json:"parent_workspace_id"`
	Branch            string     `db:"branch" json:"branch"`
	CommitSHA         string     `db:"commit_sha" json:"commit_sha"`
	Dirty             int        `db:"dirty" json:"dirty"`
	LastGitRefreshAt  *time.Time `db:"last_git_refresh_at" json:"last_git_refresh_at"`
	GitError          string     `db:"git_error" json:"git_error"`
	LastScannedAt     time.Time  `db:"last_scanned_at" json:"last_scanned_at"`
	ServerName        string     `db:"server_name" json:"server_name"`
	ProjectName       string     `db:"project_name" json:"project_name"`
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	var out []Workspace
	err := s.DB.SelectContext(ctx, &out, `SELECT w.id,w.project_id,w.server_id,w.path,w.display_name,w.management_mode,w.status,w.kind,w.parent_workspace_id,w.branch,w.commit_sha,w.dirty,w.last_git_refresh_at,w.git_error,w.last_scanned_at,s.name server_name,p.name project_name FROM workspaces w JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id ORDER BY p.name,s.name`)
	return out, err
}

func (s *Store) Workspace(ctx context.Context, id string) (Workspace, error) {
	var workspace Workspace
	err := s.DB.GetContext(ctx, &workspace, s.Q(`SELECT w.id,w.project_id,w.server_id,w.path,w.display_name,w.management_mode,w.status,w.kind,w.parent_workspace_id,w.branch,w.commit_sha,w.dirty,w.last_git_refresh_at,w.git_error,w.last_scanned_at,s.name server_name,p.name project_name FROM workspaces w JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id WHERE w.id=?`), id)
	return workspace, err
}

type WorkspaceFileSnapshot struct {
	WorkspaceID string     `db:"workspace_id" json:"workspace_id"`
	Files       string     `db:"files" json:"-"`
	Truncated   int        `db:"truncated" json:"truncated"`
	Status      string     `db:"status" json:"status"`
	Error       string     `db:"error" json:"error"`
	RequestedAt *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt   *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) WorkspaceFileSnapshot(ctx context.Context, workspaceID string) (WorkspaceFileSnapshot, error) {
	var snapshot WorkspaceFileSnapshot
	err := s.DB.GetContext(ctx, &snapshot, s.Q("SELECT workspace_id,files,truncated,status,error,requested_at,updated_at FROM workspace_file_snapshots WHERE workspace_id=?"), workspaceID)
	return snapshot, err
}

func (s *Store) BeginWorkspaceFileScan(ctx context.Context, workspaceID string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_snapshots(workspace_id,status,error,requested_at) VALUES(?,'scanning','',?) ON CONFLICT(workspace_id) DO UPDATE SET status='scanning',error='',requested_at=excluded.requested_at`), workspaceID, now)
	return err
}

func (s *Store) SaveWorkspaceFiles(ctx context.Context, workspaceID string, result protocol.WorkspaceFilesResult) error {
	files, err := json.Marshal(result.Files)
	if err != nil {
		return err
	}
	truncated := 0
	if result.Truncated {
		truncated = 1
	}
	now := time.Now().UTC()
	_, err = s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_snapshots(workspace_id,files,truncated,status,error,requested_at,updated_at) VALUES(?,?,?,'succeeded','',?,?) ON CONFLICT(workspace_id) DO UPDATE SET files=excluded.files,truncated=excluded.truncated,status='succeeded',error='',updated_at=excluded.updated_at`), workspaceID, string(files), truncated, now, now)
	return err
}

func (s *Store) FailWorkspaceFileScan(ctx context.Context, workspaceID, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_snapshots(workspace_id,status,error,requested_at) VALUES(?,'failed',?,?) ON CONFLICT(workspace_id) DO UPDATE SET status='failed',error=excluded.error`), workspaceID, message, time.Now().UTC())
	return err
}

type WorkspaceFilePreview struct {
	WorkspaceID string     `db:"workspace_id" json:"workspace_id"`
	Path        string     `db:"path" json:"path"`
	Content     string     `db:"content" json:"content"`
	Size        int64      `db:"size" json:"size"`
	Truncated   int        `db:"truncated" json:"truncated"`
	Status      string     `db:"status" json:"status"`
	Error       string     `db:"error" json:"error"`
	RequestedAt *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt   *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) WorkspaceFilePreview(ctx context.Context, workspaceID, path string) (WorkspaceFilePreview, error) {
	var preview WorkspaceFilePreview
	err := s.DB.GetContext(ctx, &preview, s.Q("SELECT workspace_id,path,content,size,truncated,status,error,requested_at,updated_at FROM workspace_file_previews WHERE workspace_id=? AND path=?"), workspaceID, path)
	return preview, err
}

func (s *Store) BeginWorkspaceFilePreview(ctx context.Context, workspaceID, path string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_previews(workspace_id,path,content,size,truncated,status,error,requested_at) VALUES(?,?,'',0,0,'loading','',?) ON CONFLICT(workspace_id) DO UPDATE SET path=excluded.path,content='',size=0,truncated=0,status='loading',error='',requested_at=excluded.requested_at,updated_at=NULL`), workspaceID, path, now)
	return err
}

func (s *Store) SaveWorkspaceFilePreview(ctx context.Context, workspaceID, path string, result protocol.WorkspaceFilePreviewResult) error {
	truncated := 0
	if result.Truncated {
		truncated = 1
	}
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspace_file_previews SET content=?,size=?,truncated=?,status='succeeded',error='',updated_at=? WHERE workspace_id=? AND path=?"), result.Content, result.Size, truncated, time.Now().UTC(), workspaceID, path)
	return err
}

func (s *Store) FailWorkspaceFilePreview(ctx context.Context, workspaceID, path, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspace_file_previews SET content='',size=0,truncated=0,status='failed',error=?,updated_at=? WHERE workspace_id=? AND path=?"), message, time.Now().UTC(), workspaceID, path)
	return err
}

type WorkspaceChangeSnapshot struct {
	WorkspaceID string     `db:"workspace_id" json:"workspace_id"`
	Changes     string     `db:"changes" json:"-"`
	Status      string     `db:"status" json:"status"`
	Error       string     `db:"error" json:"error"`
	RequestedAt *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt   *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) WorkspaceChangeSnapshot(ctx context.Context, workspaceID string) (WorkspaceChangeSnapshot, error) {
	var snapshot WorkspaceChangeSnapshot
	err := s.DB.GetContext(ctx, &snapshot, s.Q("SELECT workspace_id,changes,status,error,requested_at,updated_at FROM workspace_change_snapshots WHERE workspace_id=?"), workspaceID)
	return snapshot, err
}

func (s *Store) BeginWorkspaceChangeScan(ctx context.Context, workspaceID string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_change_snapshots(workspace_id,status,error,requested_at) VALUES(?,'scanning','',?) ON CONFLICT(workspace_id) DO UPDATE SET status='scanning',error='',requested_at=excluded.requested_at`), workspaceID, now)
	return err
}

func (s *Store) SaveWorkspaceChanges(ctx context.Context, workspaceID string, result protocol.WorkspaceChangesResult) error {
	changes, err := json.Marshal(result.Changes)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_change_snapshots(workspace_id,changes,status,error,requested_at,updated_at) VALUES(?,?,'succeeded','',?,?) ON CONFLICT(workspace_id) DO UPDATE SET changes=excluded.changes,status='succeeded',error='',updated_at=excluded.updated_at`), workspaceID, string(changes), now, now)
	return err
}

func (s *Store) FailWorkspaceChangeScan(ctx context.Context, workspaceID, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_change_snapshots(workspace_id,status,error,requested_at) VALUES(?,'failed',?,?) ON CONFLICT(workspace_id) DO UPDATE SET status='failed',error=excluded.error`), workspaceID, message, time.Now().UTC())
	return err
}

type WorkspaceDiffPreview struct {
	WorkspaceID string     `db:"workspace_id" json:"workspace_id"`
	Path        string     `db:"path" json:"path"`
	Content     string     `db:"content" json:"content"`
	Additions   int        `db:"additions" json:"additions"`
	Deletions   int        `db:"deletions" json:"deletions"`
	Binary      int        `db:"is_binary" json:"binary"`
	Truncated   int        `db:"truncated" json:"truncated"`
	Status      string     `db:"status" json:"status"`
	Error       string     `db:"error" json:"error"`
	RequestedAt *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt   *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) WorkspaceDiffPreview(ctx context.Context, workspaceID, path string) (WorkspaceDiffPreview, error) {
	var preview WorkspaceDiffPreview
	err := s.DB.GetContext(ctx, &preview, s.Q("SELECT workspace_id,path,content,additions,deletions,is_binary,truncated,status,error,requested_at,updated_at FROM workspace_diff_previews WHERE workspace_id=? AND path=?"), workspaceID, path)
	return preview, err
}

func (s *Store) BeginWorkspaceDiffPreview(ctx context.Context, workspaceID, path string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_diff_previews(workspace_id,path,content,additions,deletions,is_binary,truncated,status,error,requested_at) VALUES(?,?,'',0,0,0,0,'loading','',?) ON CONFLICT(workspace_id) DO UPDATE SET path=excluded.path,content='',additions=0,deletions=0,is_binary=0,truncated=0,status='loading',error='',requested_at=excluded.requested_at,updated_at=NULL`), workspaceID, path, now)
	return err
}

func (s *Store) SaveWorkspaceDiffPreview(ctx context.Context, workspaceID, path string, result protocol.WorkspaceDiffResult) error {
	binary, truncated := 0, 0
	if result.Binary {
		binary = 1
	}
	if result.Truncated {
		truncated = 1
	}
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspace_diff_previews SET content=?,additions=?,deletions=?,is_binary=?,truncated=?,status='succeeded',error='',updated_at=? WHERE workspace_id=? AND path=?"), result.Content, result.Additions, result.Deletions, binary, truncated, time.Now().UTC(), workspaceID, path)
	return err
}

func (s *Store) FailWorkspaceDiffPreview(ctx context.Context, workspaceID, path, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspace_diff_previews SET content='',additions=0,deletions=0,is_binary=0,truncated=0,status='failed',error=?,updated_at=? WHERE workspace_id=? AND path=?"), message, time.Now().UTC(), workspaceID, path)
	return err
}

type CodexSnapshot struct {
	ScopeType    string     `db:"scope_type" json:"scope_type"`
	ScopeID      string     `db:"scope_id" json:"scope_id"`
	Kind         string     `db:"kind" json:"kind"`
	Data         string     `db:"data" json:"-"`
	Supported    int        `db:"supported" json:"supported"`
	Reason       string     `db:"reason" json:"reason"`
	CodexVersion string     `db:"codex_version" json:"codex_version"`
	Status       string     `db:"status" json:"status"`
	Error        string     `db:"error" json:"error"`
	RequestedAt  *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt    *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) CodexSnapshot(ctx context.Context, scopeType, scopeID, kind string) (CodexSnapshot, error) {
	var snapshot CodexSnapshot
	err := s.DB.GetContext(ctx, &snapshot, s.Q("SELECT scope_type,scope_id,kind,data,supported,reason,codex_version,status,error,requested_at,updated_at FROM codex_snapshots WHERE scope_type=? AND scope_id=? AND kind=?"), scopeType, scopeID, kind)
	return snapshot, err
}

func (s *Store) BeginCodexSnapshot(ctx context.Context, scopeType, scopeID, kind string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,status,error,requested_at) VALUES(?,?,?,'loading','',?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET status='loading',error='',requested_at=excluded.requested_at`), scopeType, scopeID, kind, now)
	return err
}

func (s *Store) SaveCodexSnapshot(ctx context.Context, scopeType, scopeID, kind string, result protocol.CodexCapabilityResult) error {
	data := result.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	var valid any
	if err := json.Unmarshal(data, &valid); err != nil {
		return err
	}
	supported := 0
	if result.Supported {
		supported = 1
	}
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,data,supported,reason,codex_version,status,error,requested_at,updated_at) VALUES(?,?,?,?,?,?,?,'succeeded','',?,?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET data=excluded.data,supported=excluded.supported,reason=excluded.reason,codex_version=excluded.codex_version,status='succeeded',error='',updated_at=excluded.updated_at`), scopeType, scopeID, kind, string(data), supported, result.Reason, result.CodexVersion, now, now)
	return err
}

func (s *Store) FailCodexSnapshot(ctx context.Context, scopeType, scopeID, kind, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO codex_snapshots(scope_type,scope_id,kind,status,error,requested_at) VALUES(?,?,?,'failed',?,?) ON CONFLICT(scope_type,scope_id,kind) DO UPDATE SET status='failed',error=excluded.error,updated_at=excluded.requested_at`), scopeType, scopeID, kind, message, time.Now().UTC())
	return err
}

func (s *Store) QueueOperation(ctx context.Context, serverID, kind string, payload any, idempotency string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.queueOperationPayload(ctx, serverID, kind, string(raw), idempotency, OperationResource{}, false)
}

type OperationResource struct {
	ProjectID   string
	WorkspaceID string
}

func (s *Store) QueueResourceOperation(ctx context.Context, serverID, kind string, payload any, idempotency string, resource OperationResource, write bool) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.queueOperationPayload(ctx, serverID, kind, string(raw), idempotency, resource, write)
}

func (s *Store) QueueEncryptedOperation(ctx context.Context, serverID, kind, ciphertext, idempotency string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted operation payload must use a supported Vault format")
	}
	return s.queueOperationPayload(ctx, serverID, kind, ciphertext, idempotency, OperationResource{}, false)
}

func (s *Store) queueOperationPayload(ctx context.Context, serverID, kind, payload, idempotency string, resource OperationResource, write bool) (string, error) {
	var existing string
	err := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	resolved, err := s.resolveOperationResource(ctx, serverID, resource)
	if err != nil {
		return "", err
	}
	workspaceWrite := 0
	if write {
		if resolved.WorkspaceID == "" {
			return "", errors.New("workspace write operation requires a workspace")
		}
		workspaceWrite = 1
	}
	id := NewID()
	_, err = s.DB.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?, ?, NULLIF(?,''), NULLIF(?,''), ?, ?, ?, ?, ?) ON CONFLICT(idempotency_key) DO NOTHING"), id, serverID, resolved.ProjectID, resolved.WorkspaceID, kind, payload, workspaceWrite, idempotency, time.Now().UTC())
	if err != nil {
		if getErr := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); getErr == nil {
			return existing, nil
		}
		if write {
			active, activeErr := s.HasActiveWorkspaceWriteOperation(ctx, resolved.WorkspaceID)
			if activeErr == nil && active {
				return "", ErrWorkspaceWriteActive
			}
		}
		return "", err
	}
	if err := s.DB.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); err != nil {
		return "", err
	}
	return existing, nil
}

func (s *Store) resolveOperationResource(ctx context.Context, serverID string, resource OperationResource) (OperationResource, error) {
	if resource.WorkspaceID != "" {
		var workspace struct {
			ProjectID string `db:"project_id"`
			ServerID  string `db:"server_id"`
		}
		if err := s.DB.GetContext(ctx, &workspace, s.Q("SELECT project_id,server_id FROM workspaces WHERE id=?"), resource.WorkspaceID); err != nil {
			return OperationResource{}, err
		}
		if workspace.ServerID != serverID {
			return OperationResource{}, errors.New("operation workspace does not belong to server")
		}
		if resource.ProjectID != "" && resource.ProjectID != workspace.ProjectID {
			return OperationResource{}, errors.New("operation workspace does not belong to project")
		}
		resource.ProjectID = workspace.ProjectID
		return resource, nil
	}
	if resource.ProjectID != "" {
		var count int
		if err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM projects WHERE id=?"), resource.ProjectID); err != nil {
			return OperationResource{}, err
		}
		if count != 1 {
			return OperationResource{}, sql.ErrNoRows
		}
	}
	return resource, nil
}

type Operation struct {
	ID             string     `db:"id" json:"id"`
	ServerID       string     `db:"server_id" json:"server_id"`
	ProjectID      string     `db:"project_id" json:"project_id"`
	WorkspaceID    string     `db:"workspace_id" json:"workspace_id"`
	Kind           string     `db:"kind" json:"kind"`
	Payload        string     `db:"payload" json:"-"`
	Status         string     `db:"status" json:"status"`
	WorkspaceWrite int        `db:"workspace_write" json:"workspace_write"`
	Result         string     `db:"result" json:"result"`
	ResultData     string     `db:"result_data" json:"result_data"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	DeliveredAt    *time.Time `db:"delivered_at" json:"delivered_at"`
	CompletedAt    *time.Time `db:"completed_at" json:"completed_at"`
}

const operationSelect = `SELECT id,server_id,COALESCE(project_id,'') project_id,COALESCE(workspace_id,'') workspace_id,kind,payload,status,workspace_write,COALESCE(result,'') result,COALESCE(result_data,'{}') result_data,created_at,delivered_at,completed_at FROM agent_operations`

func (s *Store) Operation(ctx context.Context, id string) (Operation, error) {
	var operation Operation
	err := s.DB.GetContext(ctx, &operation, s.Q(operationSelect+" WHERE id=?"), id)
	return operation, err
}

func (s *Store) PendingOperations(ctx context.Context, serverID string) ([]Operation, error) {
	var out []Operation
	err := s.DB.SelectContext(ctx, &out, s.Q(operationSelect+" WHERE server_id=? AND (status='queued' OR (status='delivered' AND delivered_at<?)) ORDER BY created_at LIMIT 100"), serverID, time.Now().UTC().Add(-30*time.Second))
	return out, err
}

func (s *Store) ListProjectOperations(ctx context.Context, projectID string, limit int) ([]Operation, error) {
	limit = operationListLimit(limit)
	out := make([]Operation, 0)
	err := s.DB.SelectContext(ctx, &out, s.Q(operationSelect+` WHERE project_id=? OR workspace_id IN (SELECT id FROM workspaces WHERE project_id=?) ORDER BY created_at DESC LIMIT ?`), projectID, projectID, limit)
	return out, err
}

func (s *Store) ListWorkspaceOperations(ctx context.Context, workspaceID string, limit int) ([]Operation, error) {
	limit = operationListLimit(limit)
	var out []Operation
	err := s.DB.SelectContext(ctx, &out, s.Q(operationSelect+" WHERE workspace_id=? ORDER BY created_at DESC LIMIT ?"), workspaceID, limit)
	return out, err
}

func operationListLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func (s *Store) HasActiveWorkspaceWriteOperation(ctx context.Context, workspaceID string) (bool, error) {
	var count int
	err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND workspace_write=1 AND status IN ('queued','delivered','running')"), workspaceID)
	return count > 0, err
}

func (s *Store) HasActiveOperation(ctx context.Context, serverID, kind string) (bool, error) {
	var count int
	err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM agent_operations WHERE server_id=? AND kind=? AND status IN ('queued','delivered')"), serverID, kind)
	return count > 0, err
}

func (s *Store) Setting(ctx context.Context, key, fallback string) (string, error) {
	var value string
	err := s.DB.GetContext(ctx, &value, s.Q("SELECT value FROM control_settings WHERE key=?"), key)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return value, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO control_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`), key, value, time.Now().UTC())
	return err
}

func (s *Store) MarkDelivered(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='delivered',delivered_at=? WHERE id=? AND status='queued'"), time.Now().UTC(), id)
	return err
}
func (s *Store) CompleteOperation(ctx context.Context, r protocol.OperationResult) error {
	data, err := operationResultData(r.Data)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, s.Q("UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=? WHERE id=?"), r.Status, r.Message, data, time.Now().UTC(), r.OperationID)
	return err
}

func operationResultData(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	if !json.Valid(data) {
		return "", errors.New("operation result data must be valid JSON")
	}
	return string(data), nil
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
	result, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(event.Payload))
	if err != nil {
		return event, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return event, err
	}
	if rows == 0 {
		if err := tx.GetContext(ctx, &event.Sequence, s.Q("SELECT sequence FROM events WHERE event_id=? AND stream_id=?"), event.EventID, event.StreamID); err != nil {
			return event, err
		}
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
		EventID    string    `db:"event_id"`
		StreamID   string    `db:"stream_id"`
		Kind       string    `db:"kind"`
		Payload    string    `db:"payload"`
		Sequence   int64     `db:"sequence"`
		OccurredAt time.Time `db:"occurred_at"`
	}
	var err error
	if streamID == "" {
		err = s.DB.SelectContext(ctx, &rows, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE sequence>? ORDER BY occurred_at,sequence LIMIT ?"), after, limit)
	} else {
		err = s.DB.SelectContext(ctx, &rows, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND sequence>? ORDER BY sequence LIMIT ?"), streamID, after, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]protocol.StreamEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, protocol.StreamEvent{EventID: r.EventID, StreamID: r.StreamID, Sequence: r.Sequence, Kind: r.Kind, OccurredAt: r.OccurredAt, Payload: json.RawMessage(r.Payload)})
	}
	return out, nil
}

func (s *Store) ConversationEvents(ctx context.Context, streamID string, after int64, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events
		WHERE stream_id=? AND sequence>? AND kind IN ('user.message','codex.item.completed','codex.error','codex.turn.completed','codex.turn.failed','codex.turn.cancelled','codex.interrupt.failed','codex.approval.failed')
		ORDER BY sequence LIMIT ?`), streamID, after, limit)
}

func (s *Store) RecentEvents(ctx context.Context, streamID string, limit int) ([]protocol.StreamEvent, error) {
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	return s.eventRows(ctx, s.Q(`SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM (
		SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? ORDER BY sequence DESC LIMIT ?
	) recent ORDER BY sequence`), streamID, limit)
}

func (s *Store) eventRows(ctx context.Context, query string, args ...any) ([]protocol.StreamEvent, error) {
	var rows []struct {
		EventID    string    `db:"event_id"`
		StreamID   string    `db:"stream_id"`
		Kind       string    `db:"kind"`
		Payload    string    `db:"payload"`
		Sequence   int64     `db:"sequence"`
		OccurredAt time.Time `db:"occurred_at"`
	}
	if err := s.DB.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	out := make([]protocol.StreamEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, protocol.StreamEvent{EventID: row.EventID, StreamID: row.StreamID, Sequence: row.Sequence, Kind: row.Kind, OccurredAt: row.OccurredAt, Payload: json.RawMessage(row.Payload)})
	}
	return out, nil
}

func (s *Store) LatestActiveTurnID(ctx context.Context, threadID string) (string, error) {
	var event struct {
		Kind    string `db:"kind"`
		Payload string `db:"payload"`
	}
	if err := s.DB.GetContext(ctx, &event, s.Q("SELECT kind,payload FROM events WHERE stream_id=? AND kind IN ('turn.accepted','codex.turn.started') ORDER BY sequence DESC LIMIT 1"), threadID); err != nil {
		return "", err
	}
	var value struct {
		TurnID string `json:"turn_id"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal([]byte(event.Payload), &value); err != nil {
		return "", err
	}
	if event.Kind == "codex.turn.started" {
		return value.Turn.ID, nil
	}
	return value.TurnID, nil
}

func (s *Store) RewriteThread(ctx context.Context, thread Thread, editEventID string, command protocol.StartTurnCommand, userPayload json.RawMessage) (string, protocol.StreamEvent, error) {
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", protocol.StreamEvent{}, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, s.Q("SELECT status FROM codex_threads WHERE id=?"), thread.ID); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	if status == "queued" || status == "running" {
		return "", protocol.StreamEvent{}, ErrThreadActive
	}
	var target struct {
		Sequence int64  `db:"sequence"`
		Kind     string `db:"kind"`
	}
	if err := tx.GetContext(ctx, &target, s.Q("SELECT sequence,kind FROM events WHERE stream_id=? AND event_id=?"), thread.ID, editEventID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", protocol.StreamEvent{}, ErrInvalidEditTarget
		}
		return "", protocol.StreamEvent{}, err
	}
	if target.Kind != "user.message" {
		return "", protocol.StreamEvent{}, ErrInvalidEditTarget
	}
	var numTurns uint32
	if err := tx.GetContext(ctx, &numTurns, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND kind='turn.accepted' AND sequence>?"), thread.ID, target.Sequence); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	var cutoffSequence int64
	if err := tx.GetContext(ctx, &cutoffSequence, s.Q("SELECT COALESCE(MAX(sequence),0) FROM events WHERE stream_id=?"), thread.ID); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	replacement := protocol.StreamEvent{EventID: NewID(), StreamID: thread.ID, Sequence: target.Sequence, Kind: "user.message", OccurredAt: time.Now().UTC(), Payload: userPayload}
	rewrite := protocol.RewriteTurnCommand{
		Start:              command,
		NumTurns:           numTurns,
		EditEventID:        editEventID,
		ReplacementEventID: replacement.EventID,
		ReplacementPayload: userPayload,
		CutoffSequence:     cutoffSequence,
	}
	operationPayload, err := json.Marshal(rewrite)
	if err != nil {
		return "", protocol.StreamEvent{}, err
	}
	now := time.Now().UTC()
	operationID := NewID()
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?)"), operationID, thread.ServerID, "codex.turn.rewrite", string(operationPayload), "codex-rewrite:"+NewID(), now); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET status='queued',updated_at=? WHERE id=?"), now, thread.ID); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", protocol.StreamEvent{}, err
	}
	return operationID, replacement, nil
}

func (s *Store) CommitThreadRewrite(ctx context.Context, threadID, codexThreadID, editEventID, replacementEventID string, replacementPayload json.RawMessage, cutoffSequence int64) (protocol.StreamEvent, bool, error) {
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return protocol.StreamEvent{}, false, err
	}
	defer tx.Rollback()
	var target struct {
		Sequence int64  `db:"sequence"`
		Kind     string `db:"kind"`
	}
	if err := tx.GetContext(ctx, &target, s.Q("SELECT sequence,kind FROM events WHERE stream_id=? AND event_id=?"), threadID, editEventID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return protocol.StreamEvent{}, false, err
		}
		var existing struct {
			Sequence   int64     `db:"sequence"`
			Kind       string    `db:"kind"`
			OccurredAt time.Time `db:"occurred_at"`
			Payload    string    `db:"payload"`
		}
		if err := tx.GetContext(ctx, &existing, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND event_id=?"), threadID, replacementEventID); err != nil {
			return protocol.StreamEvent{}, false, err
		}
		return protocol.StreamEvent{EventID: replacementEventID, StreamID: threadID, Sequence: existing.Sequence, Kind: existing.Kind, OccurredAt: existing.OccurredAt, Payload: json.RawMessage(existing.Payload)}, false, nil
	}
	if target.Kind != "user.message" {
		return protocol.StreamEvent{}, false, ErrInvalidEditTarget
	}
	if cutoffSequence < target.Sequence {
		return protocol.StreamEvent{}, false, ErrInvalidEditTarget
	}
	var earlierMessages int
	if err := tx.GetContext(ctx, &earlierMessages, s.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND kind='user.message' AND sequence<?"), threadID, target.Sequence); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=? AND sequence>=? AND sequence<=?"), threadID, target.Sequence, cutoffSequence); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	now := time.Now().UTC()
	event := protocol.StreamEvent{EventID: replacementEventID, StreamID: threadID, Sequence: target.Sequence, Kind: "user.message", OccurredAt: now, Payload: replacementPayload}
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), event.EventID, event.StreamID, event.Sequence, event.Kind, event.OccurredAt, string(event.Payload)); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	title := ""
	if earlierMessages == 0 {
		title = "New session"
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE codex_threads SET codex_thread_id=?,status='running',title=CASE WHEN ?='' THEN title ELSE ? END,updated_at=? WHERE id=?"), codexThreadID, title, title, now, threadID); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.StreamEvent{}, false, err
	}
	return event, true, nil
}

func (s *Store) SaveMetrics(ctx context.Context, serverID string, m protocol.Metrics) error {
	now := time.Now().UTC().Truncate(time.Minute)
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO metric_rollups(server_id,bucket_at,resolution,cpu_percent,memory_percent,disk_percent,load_1,net_rx_bytes,net_tx_bytes,samples) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(server_id,bucket_at,resolution) DO UPDATE SET cpu_percent=(metric_rollups.cpu_percent*metric_rollups.samples+excluded.cpu_percent)/(metric_rollups.samples+1),memory_percent=(metric_rollups.memory_percent*metric_rollups.samples+excluded.memory_percent)/(metric_rollups.samples+1),disk_percent=(metric_rollups.disk_percent*metric_rollups.samples+excluded.disk_percent)/(metric_rollups.samples+1),load_1=(metric_rollups.load_1*metric_rollups.samples+excluded.load_1)/(metric_rollups.samples+1),net_rx_bytes=excluded.net_rx_bytes,net_tx_bytes=excluded.net_tx_bytes,samples=metric_rollups.samples+1`), serverID, now, "minute", m.CPUPercent, m.MemoryPercent, m.DiskPercent, m.Load1, m.NetRxBytes, m.NetTxBytes)
	return err
}

func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
