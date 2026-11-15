package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrProjectAlreadyOnServer = errors.New("project already exists on target server")

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
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockWorkspaceInventory(ctx, tx, serverID); err != nil {
		return err
	}
	var managedRootsJSON string
	if err := tx.GetContext(ctx, &managedRootsJSON, s.Q("SELECT managed_roots FROM servers WHERE id=?"), serverID); err != nil {
		return err
	}
	managedRoots := decodeManagedRoots(managedRootsJSON)
	for _, repo := range inv.Repositories {
		var excluded int
		if err := tx.GetContext(ctx, &excluded, s.Q(`SELECT COUNT(*) FROM workspace_inventory_exclusions e WHERE e.server_id=? AND e.path=?
			AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.server_id=e.server_id AND w.path=e.path)`), serverID, repo.Path); err != nil {
			return err
		}
		if excluded != 0 {
			continue
		}
		normalized := normalizeRemote(repo.RemoteURL)
		var remoteProjectID string
		if normalized != "" {
			err := tx.GetContext(ctx, &remoteProjectID, s.Q("SELECT id FROM projects WHERE normalized_remote=?"), normalized)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var pathProjectID string
		err := tx.GetContext(ctx, &pathProjectID, s.Q("SELECT project_id FROM workspaces WHERE server_id=? AND path=?"), serverID, repo.Path)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		projectID := remoteProjectID
		if projectID == "" {
			projectID = pathProjectID
			if projectID != "" && normalized != "" {
				if err := s.backfillInventoryProjectRemote(ctx, tx, projectID, repo.RemoteURL, normalized); err != nil {
					return err
				}
			}
		}
		if projectID == "" {
			projectID = NewID()
			if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO projects(id,name,remote_url,normalized_remote) VALUES(?,?,?,?)"), projectID, repo.Name, repo.RemoteURL, normalized); err != nil {
				return err
			}
		}
		if err := s.ensureProjectRemote(ctx, tx, projectID, repo.RemoteURL); err != nil {
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
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id,path) DO UPDATE SET project_id=excluded.project_id,management_mode=CASE WHEN excluded.management_mode='managed' THEN 'managed' ELSE workspaces.management_mode END,branch=excluded.branch,commit_sha=excluded.commit_sha,dirty=excluded.dirty,status='ready',last_git_refresh_at=excluded.last_git_refresh_at,git_error='',last_scanned_at=excluded.last_scanned_at`), workspaceID, projectID, serverID, repo.Path, repo.Name, managementMode, repo.Branch, repo.CommitSHA, dirty, now, now)
		if err != nil {
			return err
		}
		if remoteProjectID != "" && pathProjectID != "" && pathProjectID != remoteProjectID {
			if err := s.mergeInventoryProjectIfOrphaned(ctx, tx, pathProjectID, remoteProjectID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) backfillInventoryProjectRemote(ctx context.Context, tx *sqlx.Tx, projectID, remoteURL, normalized string) error {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" || normalized == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, s.Q(`UPDATE projects SET remote_url=?,normalized_remote=?,updated_at=? WHERE id=? AND normalized_remote=''`), remoteURL, normalized, time.Now().UTC(), projectID)
	return err
}

func (s *Store) mergeInventoryProjectIfOrphaned(ctx context.Context, tx *sqlx.Tx, sourceProjectID, targetProjectID string) error {
	if sourceProjectID == "" || targetProjectID == "" || sourceProjectID == targetProjectID {
		return nil
	}
	var remainingWorkspaces int
	if err := tx.GetContext(ctx, &remainingWorkspaces, s.Q("SELECT COUNT(*) FROM workspaces WHERE project_id=?"), sourceProjectID); err != nil {
		return err
	}
	if remainingWorkspaces != 0 {
		return nil
	}
	var deploymentTargets int
	if err := tx.GetContext(ctx, &deploymentTargets, s.Q("SELECT COUNT(*) FROM deployment_targets WHERE project_id=?"), sourceProjectID); err != nil {
		return err
	}
	if deploymentTargets != 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET project_id=? WHERE project_id=?"), targetProjectID, sourceProjectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM project_remotes WHERE project_id=? AND name IN (SELECT name FROM project_remotes WHERE project_id=?)`), sourceProjectID, targetProjectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE project_remotes SET project_id=?,updated_at=? WHERE project_id=?"), targetProjectID, time.Now().UTC(), sourceProjectID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.Q("DELETE FROM projects WHERE id=?"), sourceProjectID)
	return err
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

// likePatternEscaper makes user input match literally in a LIKE ... ESCAPE '\'
// pattern; SQLite and PostgreSQL both honour that escape clause.
var likePatternEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	return s.ListProjectsFiltered(ctx, "", "", "")
}

func (s *Store) ListProjectsFiltered(ctx context.Context, name, serverID, status string) ([]Project, error) {
	filters := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if name = strings.TrimSpace(name); name != "" {
		filters = append(filters, `LOWER(p.name) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+likePatternEscaper.Replace(name)+"%")
	}
	if serverID = strings.TrimSpace(serverID); serverID != "" {
		filters = append(filters, `(EXISTS (SELECT 1 FROM workspaces sw WHERE sw.project_id=p.id AND sw.server_id=?)
			OR EXISTS (SELECT 1 FROM agent_operations so WHERE so.project_id=p.id AND so.server_id=? AND so.kind IN ('git.import','git.project.create')))`)
		args = append(args, serverID, serverID)
	}
	if status = strings.TrimSpace(status); status != "" {
		filters = append(filters, "p.status=?")
		args = append(args, status)
	}
	where := ""
	if len(filters) > 0 {
		where = " WHERE " + strings.Join(filters, " AND ")
	}
	var out []Project
	if err := s.DB.SelectContext(ctx, &out, s.Q(`SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,(SELECT COUNT(*) FROM workspaces w WHERE w.project_id=p.id) workspace_count FROM projects p`+where+` ORDER BY CASE WHEN p.pinned_at IS NULL THEN 1 ELSE 0 END,p.pinned_at DESC,p.name`), args...); err != nil {
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
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("UPDATE projects SET "+strings.Join(sets, ",")+" WHERE id=?"), args...)
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
	// The project management page provides the global recovery path for hidden
	// projects, including server-specific hides made from the session sidebar.
	if hidden != nil && !*hidden {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE project_server_preferences SET hidden_at=NULL WHERE project_id=?"), id); err != nil {
			return Project{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
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

func (s *Store) listProjectImports(ctx context.Context, projectID ...string) ([]ProjectImportOperation, error) {
	var rows []struct {
		ID         string    `db:"id"`
		ServerID   string    `db:"server_id"`
		ServerName string    `db:"server_name"`
		Status     string    `db:"status"`
		Message    string    `db:"message"`
		Payload    string    `db:"payload"`
		CreatedAt  time.Time `db:"created_at"`
	}
	query := `SELECT o.id,o.server_id,s.name server_name,o.status,COALESCE(o.result,'') message,o.payload,o.created_at FROM projects p JOIN agent_operations o ON o.id=(SELECT newest.id FROM agent_operations newest WHERE newest.kind='git.import' AND newest.project_id=p.id ORDER BY newest.created_at DESC,newest.id DESC LIMIT 1) JOIN servers s ON s.id=o.server_id WHERE 1=1`
	var args []any
	if len(projectID) > 0 {
		query += " AND p.id=?"
		args = append(args, projectID[0])
	}
	query += ` ORDER BY o.created_at DESC,o.id DESC`
	if err := s.DB.SelectContext(ctx, &rows, s.Q(query), args...); err != nil {
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
	operations, err := s.listProjectImports(ctx, projectID)
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
	var count int
	err := s.DB.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM agent_operations WHERE kind='git.import' AND project_id=? AND status IN ('queued','delivered','running')"), projectID)
	return count > 0, err
}

func (s *Store) DeleteProject(ctx context.Context, projectID string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, s.Q(`DELETE FROM projects WHERE id=? AND NOT EXISTS (SELECT 1 FROM workspaces WHERE project_id=projects.id) AND NOT EXISTS (SELECT 1 FROM deployment_targets WHERE project_id=projects.id)`), projectID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *Store) CreateProject(ctx context.Context, name, remoteURL string) (Project, error) {
	id := NewID()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO projects(id,name,remote_url,normalized_remote) VALUES(?,?,?,?)"), id, name, remoteURL, normalizeRemote(remoteURL))
	if err != nil {
		return Project{}, err
	}
	if err := s.ensureProjectRemote(ctx, tx, id, remoteURL); err != nil {
		return Project{}, err
	}
	var project Project
	if err = tx.GetContext(ctx, &project, s.Q("SELECT id,name,description,remote_url,default_branch,status,provision_error,pinned_at,hidden_at,archived_at,updated_at,0 workspace_count FROM projects WHERE id=?"), id); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return project, nil
}

func (s *Store) ProjectByRemote(ctx context.Context, remoteURL string) (Project, error) {
	var project Project
	err := s.DB.GetContext(ctx, &project, s.Q(`SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,(SELECT COUNT(*) FROM workspaces w WHERE w.project_id=p.id) workspace_count FROM projects p WHERE p.normalized_remote=?`), normalizeRemote(remoteURL))
	return project, err
}

// QueueProjectImport reserves the global project record and queues an import
// for a specific server in one transaction. A remote repository is one
// project, while each server gets its own import operation and workspace.
func (s *Store) QueueProjectImport(ctx context.Context, serverID, name, remoteURL, destination string) (Project, string, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	normalized := normalizeRemote(remoteURL)
	if normalized == "" {
		return Project{}, "", errors.New("project import requires a remote URL")
	}
	destination = strings.TrimSpace(destination)

	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Project{}, "", err
	}
	defer tx.Rollback()

	// ON CONFLICT handles two servers reserving the same remote concurrently.
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO projects(id,name,remote_url,normalized_remote) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`), NewID(), name, remoteURL, normalized); err != nil {
		return Project{}, "", err
	}
	projectQuery := `SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,(SELECT COUNT(*) FROM workspaces w WHERE w.project_id=p.id) workspace_count FROM projects p WHERE p.normalized_remote=?`
	if s.driver == "pgx" {
		projectQuery += " FOR UPDATE"
	}
	var project Project
	if err := tx.GetContext(ctx, &project, s.Q(projectQuery), normalized); err != nil {
		return Project{}, "", err
	}
	if err := s.ensureProjectRemote(ctx, tx, project.ID, project.RemoteURL); err != nil {
		return Project{}, "", err
	}
	var workspaceCount int
	if err := tx.GetContext(ctx, &workspaceCount, s.Q("SELECT COUNT(*) FROM workspaces WHERE project_id=? AND server_id=?"), project.ID, serverID); err != nil {
		return Project{}, "", err
	}
	if workspaceCount > 0 {
		return Project{}, "", ErrProjectAlreadyOnServer
	}
	var activeOperationID string
	err = tx.GetContext(ctx, &activeOperationID, s.Q("SELECT id FROM agent_operations WHERE project_id=? AND server_id=? AND kind='git.import' AND status IN ('queued','delivered') ORDER BY created_at DESC LIMIT 1"), project.ID, serverID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Project{}, "", err
		}
		return project, activeOperationID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Project{}, "", err
	}

	command := protocol.GitImportCommand{ProjectID: project.ID, Name: project.Name, RemoteURL: project.RemoteURL, Destination: destination}
	payload, err := json.Marshal(command)
	if err != nil {
		return Project{}, "", err
	}
	operationID := NewID()
	idempotency := "git-import:" + project.ID + ":" + serverID + ":" + operationID
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`), operationID, serverID, project.ID, protocol.OpGitImport, string(payload), idempotency, time.Now().UTC()); err != nil {
		return Project{}, "", err
	}
	if err := tx.GetContext(ctx, &operationID, s.Q("SELECT id FROM agent_operations WHERE project_id=? AND server_id=? AND kind='git.import' AND status IN ('queued','delivered') ORDER BY created_at DESC LIMIT 1"), project.ID, serverID); err != nil {
		return Project{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, "", err
	}
	return project, operationID, nil
}
