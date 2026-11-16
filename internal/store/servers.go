package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
)

var ErrControlPlaneServer = errors.New("the control-plane server cannot be revoked")

const ServerOnlineGracePeriod = 90 * time.Second

const (
	ControlPlaneServerID      = "control-plane-agent"
	ControlPlaneServerName    = "Control Plane"
	ControlPlaneAgentTokenKey = "control_plane_agent_token"
)

type Server struct {
	DeclaredCapabilities capabilities.Declaration `db:"declared_capabilities" json:"-"`

	ID                         string              `db:"id" json:"id"`
	Name                       string              `db:"name" json:"name"`
	Hostname                   string              `db:"hostname" json:"hostname"`
	IsControlPlane             bool                `db:"is_control_plane" json:"is_control_plane"`
	Status                     string              `db:"status" json:"status"`
	AgentVersion               string              `db:"agent_version" json:"agent_version"`
	CodexVersion               string              `db:"codex_version" json:"codex_version"`
	CodexReady                 int                 `db:"codex_ready" json:"codex_ready"`
	GeminiVersion              string              `db:"gemini_version" json:"gemini_version"`
	GeminiReady                int                 `db:"gemini_ready" json:"gemini_ready"`
	ClaudeVersion              string              `db:"claude_version" json:"claude_version"`
	ClaudeReady                int                 `db:"claude_ready" json:"claude_ready"`
	ScanRoots                  string              `db:"scan_roots" json:"-"`
	ManagedRoots               string              `db:"managed_roots" json:"-"`
	Address                    string              `db:"address" json:"address"`
	Configuration              string              `db:"configuration" json:"configuration"`
	Notes                      string              `db:"notes" json:"notes"`
	LastSeenAt                 *time.Time          `db:"last_seen_at" json:"last_seen_at"`
	CreatedAt                  time.Time           `db:"created_at" json:"created_at"`
	AgentTargetVersion         string              `db:"-" json:"agent_target_version"`
	AgentUpdateAvailable       bool                `db:"-" json:"agent_update_available"`
	AgentUpdateSupported       bool                `db:"-" json:"agent_update_supported"`
	CodexTargetVersion         string              `db:"-" json:"codex_target_version"`
	CodexUpdateAvailable       bool                `db:"-" json:"codex_update_available"`
	CodexUpdateSupported       bool                `db:"-" json:"codex_update_supported"`
	CLIManagementSupported     bool                `db:"-" json:"cli_management_supported"`
	ExactRollbackSupported     bool                `db:"-" json:"exact_rollback_supported"`
	ScopedCredentialsSupported bool                `db:"-" json:"scoped_credentials_supported"`
	GeminiCredentialsSupported bool                `db:"-" json:"gemini_credentials_supported"`
	ClaudeEffortSupported      bool                `db:"-" json:"claude_effort_supported"`
	ClaudeCredentialsSupported bool                `db:"-" json:"claude_credentials_supported"`
	ClaudeProfileID            string              `db:"claude_profile_id" json:"claude_profile_id"`
	ClaudeProfileName          string              `db:"claude_profile_name" json:"claude_profile_name"`
	ClaudeProfileModel         string              `db:"claude_profile_model" json:"claude_profile_model"`
	GeminiProfileID            string              `db:"gemini_profile_id" json:"gemini_profile_id"`
	GeminiProfileName          string              `db:"gemini_profile_name" json:"gemini_profile_name"`
	CodexProfileID             string              `db:"codex_profile_id" json:"codex_profile_id"`
	CodexProfileName           string              `db:"codex_profile_name" json:"codex_profile_name"`
	GitProfileID               string              `db:"git_profile_id" json:"git_profile_id"`
	GitProfileName             string              `db:"git_profile_name" json:"git_profile_name"`
	GitProfiles                []CredentialProfile `db:"-" json:"git_profiles"`
}

type ServerMetadata struct {
	Address       string `db:"address" json:"address"`
	Configuration string `db:"configuration" json:"configuration"`
	Notes         string `db:"notes" json:"notes"`
}

func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	var out []Server
	err := s.DB.SelectContext(ctx, &out, s.Q(`SELECT s.id,s.name,s.hostname,s.is_control_plane,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END status,s.agent_version,`+serverCapabilitySelect+`,`+codexEngineVersion+` AS codex_version,`+codexEngineReady+` AS codex_ready,`+geminiEngineVersion+` AS gemini_version,`+geminiEngineReady+` AS gemini_ready,COALESCE(ccli.version,'') claude_version,COALESCE(ccli.ready,0) claude_ready,s.scan_roots,s.managed_roots,COALESCE(m.address,'') address,COALESCE(m.configuration,'') configuration,COALESCE(m.notes,'') notes,s.last_seen_at,s.created_at,COALESCE(gcp.profile_id,'') gemini_profile_id,COALESCE(gemini.name,'') gemini_profile_name,COALESCE(ccp.profile_id,'') claude_profile_id,COALESCE(claude.name,'') claude_profile_name,COALESCE(ccp.model,'') claude_profile_model,COALESCE(ce.credential_profile_id,'') codex_profile_id,COALESCE(codex.name,'') codex_profile_name,COALESCE(cp.git_profile_id,'') git_profile_id,COALESCE(git.name,'') git_profile_name FROM servers s LEFT JOIN `+claudeEngineCLI+` ccli ON ccli.server_id=s.id LEFT JOIN `+claudeEngineBinding+` ccp ON ccp.server_id=s.id LEFT JOIN credential_profiles claude ON claude.id=ccp.profile_id LEFT JOIN `+geminiEngineBinding+` gcp ON gcp.server_id=s.id LEFT JOIN credential_profiles gemini ON gemini.id=gcp.profile_id LEFT JOIN server_metadata m ON m.server_id=s.id LEFT JOIN server_credential_profiles cp ON cp.server_id=s.id LEFT JOIN server_engines ce ON ce.server_id=s.id AND ce.engine='codex' LEFT JOIN credential_profiles codex ON codex.id=ce.credential_profile_id LEFT JOIN credential_profiles git ON git.id=cp.git_profile_id WHERE s.revoked_at IS NULL ORDER BY s.is_control_plane DESC,s.name`), time.Now().UTC().Add(-ServerOnlineGracePeriod))
	if err != nil {
		return nil, err
	}
	return out, s.loadServerGitCredentialProfiles(ctx, out)
}

func (s *Store) Server(ctx context.Context, id string) (Server, error) {
	var server Server
	err := s.DB.GetContext(ctx, &server, s.Q(`SELECT s.id,s.name,s.hostname,s.is_control_plane,CASE WHEN s.last_seen_at>? THEN 'online' ELSE 'offline' END status,s.agent_version,`+serverCapabilitySelect+`,`+codexEngineVersion+` AS codex_version,`+codexEngineReady+` AS codex_ready,`+geminiEngineVersion+` AS gemini_version,`+geminiEngineReady+` AS gemini_ready,COALESCE(ccli.version,'') claude_version,COALESCE(ccli.ready,0) claude_ready,s.scan_roots,s.managed_roots,COALESCE(m.address,'') address,COALESCE(m.configuration,'') configuration,COALESCE(m.notes,'') notes,s.last_seen_at,s.created_at,COALESCE(gcp.profile_id,'') gemini_profile_id,COALESCE(gemini.name,'') gemini_profile_name,COALESCE(ccp.profile_id,'') claude_profile_id,COALESCE(claude.name,'') claude_profile_name,COALESCE(ccp.model,'') claude_profile_model,COALESCE(ce.credential_profile_id,'') codex_profile_id,COALESCE(codex.name,'') codex_profile_name,COALESCE(cp.git_profile_id,'') git_profile_id,COALESCE(git.name,'') git_profile_name FROM servers s LEFT JOIN `+claudeEngineCLI+` ccli ON ccli.server_id=s.id LEFT JOIN `+claudeEngineBinding+` ccp ON ccp.server_id=s.id LEFT JOIN credential_profiles claude ON claude.id=ccp.profile_id LEFT JOIN `+geminiEngineBinding+` gcp ON gcp.server_id=s.id LEFT JOIN credential_profiles gemini ON gemini.id=gcp.profile_id LEFT JOIN server_metadata m ON m.server_id=s.id LEFT JOIN server_credential_profiles cp ON cp.server_id=s.id LEFT JOIN server_engines ce ON ce.server_id=s.id AND ce.engine='codex' LEFT JOIN credential_profiles codex ON codex.id=ce.credential_profile_id LEFT JOIN credential_profiles git ON git.id=cp.git_profile_id WHERE s.id=? AND s.revoked_at IS NULL`), time.Now().UTC().Add(-ServerOnlineGracePeriod), id)
	if err != nil {
		return Server{}, err
	}
	var profiles []CredentialProfile
	if err := s.DB.SelectContext(ctx, &profiles, s.Q(`SELECT p.id,p.kind,p.name,p.endpoint,p.username,p.model,p.commit_name,p.commit_email,p.updated_at
		FROM server_git_credential_profiles binding JOIN credential_profiles p ON p.id=binding.profile_id
		WHERE binding.server_id=? ORDER BY binding.position,p.name`), server.ID); err != nil {
		return Server{}, err
	}
	server.GitProfiles = profiles
	return server, nil
}

func (s *Store) loadServerGitCredentialProfiles(ctx context.Context, servers []Server) error {
	for index := range servers {
		var profiles []CredentialProfile
		if err := s.DB.SelectContext(ctx, &profiles, s.Q(`SELECT p.id,p.kind,p.name,p.endpoint,p.username,p.model,p.commit_name,p.commit_email,p.updated_at
			FROM server_git_credential_profiles binding JOIN credential_profiles p ON p.id=binding.profile_id
			WHERE binding.server_id=? ORDER BY binding.position,p.name`), servers[index].ID); err != nil {
			return err
		}
		servers[index].GitProfiles = profiles
	}
	return nil
}

// EnsureControlPlaneServer creates the built-in Agent registration once and
// restores it if an older database was left with the record revoked. The
// caller owns the clear-text token; only its hash is stored with the Agent
// credentials.
func (s *Store) EnsureControlPlaneServer(ctx context.Context, hostname, agentToken string) (Server, error) {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		hostname = ControlPlaneServerName
	}
	if strings.TrimSpace(agentToken) == "" {
		return Server{}, errors.New("control-plane agent token is required")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Server{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO servers(id,name,hostname,status,scan_roots,is_control_plane) VALUES(?,?,?,'offline','[]',1)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,hostname=excluded.hostname,is_control_plane=1,revoked_at=NULL`), ControlPlaneServerID, ControlPlaneServerName, hostname); err != nil {
		return Server{}, err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_credentials(server_id,token_hash,created_at,revoked_at) VALUES(?,?,?,NULL)
		ON CONFLICT(server_id) DO UPDATE SET token_hash=excluded.token_hash,revoked_at=NULL`), ControlPlaneServerID, HashToken(agentToken), time.Now().UTC()); err != nil {
		return Server{}, err
	}
	if err = tx.Commit(); err != nil {
		return Server{}, err
	}
	return s.Server(ctx, ControlPlaneServerID)
}

func (s *Store) RevokeServer(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var isControlPlane int
	if err = tx.GetContext(ctx, &isControlPlane, s.Q("SELECT is_control_plane FROM servers WHERE id=? AND revoked_at IS NULL"), id); err != nil {
		return err
	}
	if isControlPlane != 0 {
		return ErrControlPlaneServer
	}
	if err := s.deleteServerRecords(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) purgeRevokedServers(ctx context.Context) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var serverIDs []string
	if err := tx.SelectContext(ctx, &serverIDs, "SELECT id FROM servers WHERE revoked_at IS NOT NULL AND is_control_plane=0"); err != nil {
		return err
	}
	for _, serverID := range serverIDs {
		if err := s.deleteServerRecords(ctx, tx, serverID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) deleteServerRecords(ctx context.Context, tx *sqlx.Tx, serverID string) error {
	var projectIDs []string
	if err := tx.SelectContext(ctx, &projectIDs, s.Q(`SELECT project_id FROM workspaces WHERE server_id=?
		UNION SELECT project_id FROM deployment_targets WHERE server_id=?
		UNION SELECT project_id FROM agent_operations WHERE server_id=? AND project_id IS NOT NULL`), serverID, serverID, serverID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM events WHERE stream_id IN (
		SELECT t.id FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id WHERE w.server_id=?)`), serverID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM codex_snapshots WHERE
		(scope_type='workspace' AND scope_id IN (SELECT id FROM workspaces WHERE server_id=?)) OR
		(scope_type='thread' AND scope_id IN (SELECT t.id FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id WHERE w.server_id=?))`), serverID, serverID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.Q("DELETE FROM servers WHERE id=? AND is_control_plane=0"), serverID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return sql.ErrNoRows
	}
	for _, projectID := range projectIDs {
		if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM projects WHERE id=?
			AND NOT EXISTS (SELECT 1 FROM workspaces WHERE project_id=projects.id)
			AND NOT EXISTS (SELECT 1 FROM deployment_targets WHERE project_id=projects.id)`), projectID); err != nil {
			return err
		}
	}
	return nil
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
	result, err := tx.ExecContext(ctx, s.Q("UPDATE enrollment_tokens SET consumed_at=? WHERE id=? AND consumed_at IS NULL"), time.Now().UTC(), e.ID)
	if err != nil {
		return Enrollment{}, err
	}
	// Concurrent consumers can all pass the SELECT above; only the one whose
	// UPDATE changed the row owns the token.
	rows, err := result.RowsAffected()
	if err != nil {
		return Enrollment{}, err
	}
	if rows != 1 {
		return Enrollment{}, sql.ErrNoRows
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
	result, err := tx.ExecContext(ctx, s.Q("UPDATE servers SET hostname=?,scan_roots=?,status='offline',agent_version='',last_seen_at=NULL WHERE id=? AND revoked_at IS NULL"), hostname, e.ScanRoots, e.ServerID)
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
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE server_engines SET cli_version='',ready=0 WHERE server_id=? AND engine IN ('codex','gemini')"), e.ServerID); err != nil {
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
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ready := 0
	if h.CodexReady {
		ready = 1
	}
	geminiReady := 0
	if h.GeminiReady {
		geminiReady = 1
	}
	roots, _ := json.Marshal(h.ScanRoots)
	managedRoots, _ := json.Marshal(h.ManagedRoots)
	_, err = tx.ExecContext(ctx, s.Q(`UPDATE servers SET hostname=?,status='online',agent_version=?,scan_roots=?,managed_roots=?,last_seen_at=? WHERE id=? AND revoked_at IS NULL`), h.Hostname, h.AgentVersion, string(roots), string(managedRoots), time.Now().UTC(), serverID)
	if err != nil {
		return err
	}
	claudeReady := 0
	if h.ClaudeReady {
		claudeReady = 1
	}
	for _, engine := range []struct {
		name, version string
		ready         int
	}{{"codex", h.CodexVersion, ready}, {"gemini", h.GeminiVersion, geminiReady}, {"claude", h.ClaudeVersion, claudeReady}} {
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_engines(server_id,engine,cli_version,ready) SELECT id,?,?,? FROM servers WHERE id=? AND revoked_at IS NULL ON CONFLICT(server_id,engine) DO UPDATE SET cli_version=excluded.cli_version,ready=excluded.ready`), engine.name, engine.version, engine.ready, serverID)
		if err != nil {
			return err
		}
	}
	if h.Capabilities == nil {
		_, err = tx.ExecContext(ctx, s.Q("DELETE FROM server_capabilities WHERE server_id=?"), serverID)
	} else {
		declaration, marshalErr := json.Marshal(h.Capabilities)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO server_capabilities(server_id,capabilities) SELECT id,? FROM servers WHERE id=? AND revoked_at IS NULL ON CONFLICT(server_id) DO UPDATE SET capabilities=excluded.capabilities`), string(declaration), serverID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
