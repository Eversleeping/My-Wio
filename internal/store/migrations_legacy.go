package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/jmoiron/sqlx"
)

func migrateSecretSetVersions(ctx context.Context, db *sqlx.DB) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO secret_set_versions(secret_set_id,key_version,ciphertext,updated_at)
		SELECT id,key_version,ciphertext,updated_at FROM secret_sets WHERE 1=1
		ON CONFLICT(secret_set_id,key_version) DO NOTHING`); err != nil {
		return fmt.Errorf("backfill secret set versions: %w", err)
	}
	return nil
}

func migrateServerControlPlane(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, `ALTER TABLE servers
			ADD COLUMN IF NOT EXISTS is_control_plane INTEGER NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMP`); err != nil {
			return fmt.Errorf("migrate server lifecycle columns: %w", err)
		}
		return nil
	}
	var columns []string
	if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('servers')"); err != nil {
		return fmt.Errorf("inspect server lifecycle columns: %w", err)
	}
	existing := make(map[string]bool, len(columns))
	for _, column := range columns {
		existing[column] = true
	}
	if !existing["is_control_plane"] {
		if _, err := db.ExecContext(ctx, "ALTER TABLE servers ADD COLUMN is_control_plane INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("migrate control-plane server flag: %w", err)
		}
	}
	if !existing["revoked_at"] {
		if _, err := db.ExecContext(ctx, "ALTER TABLE servers ADD COLUMN revoked_at TIMESTAMP"); err != nil {
			return fmt.Errorf("migrate server revocation timestamp: %w", err)
		}
	}
	return nil
}

func migrateServerGitCredentialProfiles(ctx context.Context, db *sqlx.DB, driver string) error {
	if driver == "pgx" {
		if _, err := db.ExecContext(ctx, "ALTER TABLE server_credential_updates ADD COLUMN IF NOT EXISTS git_profile_ids TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return fmt.Errorf("migrate Git credential update profiles: %w", err)
		}
	} else {
		var count int
		if err := db.GetContext(ctx, &count, "SELECT COUNT(*) FROM pragma_table_info('server_credential_updates') WHERE name='git_profile_ids'"); err != nil {
			return fmt.Errorf("inspect Git credential update profiles: %w", err)
		}
		if count == 0 {
			if _, err := db.ExecContext(ctx, "ALTER TABLE server_credential_updates ADD COLUMN git_profile_ids TEXT NOT NULL DEFAULT '[]'"); err != nil {
				return fmt.Errorf("migrate Git credential update profiles: %w", err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO server_git_credential_profiles(server_id,profile_id,position)
		SELECT server_id,git_profile_id,0 FROM server_credential_profiles WHERE git_profile_id IS NOT NULL
		ON CONFLICT(server_id,profile_id) DO NOTHING`); err != nil {
		return fmt.Errorf("migrate bound Git credential profiles: %w", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE server_credential_updates
		SET git_profile_ids=CASE WHEN git_profile_id IS NULL THEN '[]' ELSE '["' || git_profile_id || '"]' END
		WHERE git_profile_ids='[]'`); err != nil {
		return fmt.Errorf("migrate pending Git credential profiles: %w", err)
	}
	return nil
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
		if _, err := db.ExecContext(ctx, `ALTER TABLE deployment_targets
			ADD COLUMN IF NOT EXISTS public_url TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS detected_public_url TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate deployment public URL: %w", err)
		}
		return nil
	}
	var columns []string
	if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info('deployment_targets')"); err != nil {
		return fmt.Errorf("inspect deployment public URL columns: %w", err)
	}
	existing := make(map[string]bool, len(columns))
	for _, column := range columns {
		existing[column] = true
	}
	for _, column := range []string{"public_url", "detected_public_url"} {
		if !existing[column] {
			if _, err := db.ExecContext(ctx, "ALTER TABLE deployment_targets ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("migrate deployment public URL column %s: %w", column, err)
			}
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
				ADD COLUMN IF NOT EXISTS thread_id TEXT REFERENCES codex_threads(id) ON DELETE SET NULL,
				ADD COLUMN IF NOT EXISTS workspace_write INTEGER NOT NULL DEFAULT 0,
				ADD COLUMN IF NOT EXISTS started_at TIMESTAMP,
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
				"thread_id":       "TEXT REFERENCES codex_threads(id) ON DELETE SET NULL",
				"workspace_write": "INTEGER NOT NULL DEFAULT 0",
				"started_at":      "TIMESTAMP",
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
		"CREATE INDEX IF NOT EXISTS operations_thread_idx ON agent_operations(thread_id, created_at)",
		"CREATE UNIQUE INDEX IF NOT EXISTS operations_project_import_active_unique ON agent_operations(server_id,project_id) WHERE project_id IS NOT NULL AND kind='git.import' AND status IN ('queued','delivered')",
		"CREATE UNIQUE INDEX IF NOT EXISTS operations_workspace_exclusive_write_unique ON agent_operations(workspace_id) WHERE workspace_id IS NOT NULL AND workspace_write=1 AND kind NOT IN ('claude.turn.start','gemini.turn.start','gemini.turn.rewrite') AND status IN ('queued','delivered','running')",
		// Install the replacement before removing the legacy index. Agent turns
		// retain workspace_write=1 for lifecycle and recovery checks, but can now
		// coexist. Transactional workspace checks exclude them from other writers.
		"DROP INDEX IF EXISTS operations_workspace_active_write_unique",
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
		if _, err := db.ExecContext(ctx, `ALTER TABLE gemini_threads ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMP`); err != nil {
			return err
		}
		return nil
	}
	for _, table := range []struct {
		name    string
		columns []string
	}{
		{name: "projects", columns: []string{"pinned_at", "hidden_at"}},
		{name: "codex_threads", columns: []string{"pinned_at", "archived_at"}},
		{name: "gemini_threads", columns: []string{"pinned_at"}},
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

func migrateGeminiThinking(ctx context.Context, db *sqlx.DB, driver string) error {
	for _, table := range []string{"gemini_threads", "gemini_scheduled_tasks", "codex_scheduled_tasks"} {
		if driver == "pgx" {
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN IF NOT EXISTS thinking_level TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		} else {
			var columns []string
			if err := db.SelectContext(ctx, &columns, "SELECT name FROM pragma_table_info(?)", table); err != nil {
				return err
			}
			found := false
			for _, column := range columns {
				if column == "thinking_level" {
					found = true
				}
			}
			if !found {
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN thinking_level TEXT NOT NULL DEFAULT ''"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
