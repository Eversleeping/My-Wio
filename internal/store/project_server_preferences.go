package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// An absent row inherits legacy project preferences. A present row can explicitly
// clear a legacy pin/hidden flag without affecting another server's project.
// Keep timestamp columns as direct projections: SQLite loses their declared
// timestamp type if the legacy fallback is implemented with CASE/COALESCE.
const projectServerJoin = ` LEFT JOIN (
 SELECT project_id,server_id,name,pinned_at,hidden_at FROM project_server_preferences
 UNION ALL
 SELECT DISTINCT p.id,w.server_id,'',p.pinned_at,p.hidden_at FROM projects p JOIN workspaces w ON w.project_id=p.id
 WHERE NOT EXISTS (SELECT 1 FROM project_server_preferences saved WHERE saved.project_id=p.id AND saved.server_id=w.server_id)
) ps ON ps.project_id=w.project_id AND ps.server_id=w.server_id`
const projectServerName = `COALESCE(NULLIF(ps.name,''),p.name)`
const projectServerPin = `ps.pinned_at`
const projectServerHidden = `ps.hidden_at`

type HiddenServerProject struct {
	ProjectID   string    `db:"project_id" json:"project_id"`
	ServerID    string    `db:"server_id" json:"server_id"`
	ProjectName string    `db:"project_name" json:"project_name"`
	ServerName  string    `db:"server_name" json:"server_name"`
	HiddenAt    time.Time `db:"hidden_at" json:"hidden_at"`
}

func (s *Store) HiddenServerProjects(ctx context.Context) ([]HiddenServerProject, error) {
	out := []HiddenServerProject{}
	err := s.DB.SelectContext(ctx, &out, `SELECT DISTINCT w.project_id,w.server_id,`+projectServerName+` AS project_name,s.name AS server_name,ps.hidden_at
	 FROM workspaces w JOIN projects p ON p.id=w.project_id JOIN servers s ON s.id=w.server_id`+projectServerJoin+`
	 WHERE s.revoked_at IS NULL AND ps.hidden_at IS NOT NULL ORDER BY s.name,project_name,w.project_id`)
	return out, err
}

// Creating a session is an explicit request to show this server's project again.
// Run in the creation transaction so a failed insert cannot change visibility.
func (s *Store) showProjectForNewThread(ctx context.Context, tx *sqlx.Tx, workspaceID string) error {
	_, err := tx.ExecContext(ctx, s.Q(`INSERT INTO project_server_preferences(project_id,server_id,pinned_at,hidden_at)
	 SELECT w.project_id,w.server_id,ps.pinned_at,NULL FROM workspaces w JOIN projects p ON p.id=w.project_id`+projectServerJoin+`
	 WHERE w.id=? AND ps.hidden_at IS NOT NULL
	 ON CONFLICT(project_id,server_id) DO UPDATE SET hidden_at=NULL`), workspaceID)
	return err
}

func (s *Store) ProjectOnServer(ctx context.Context, projectID, serverID string) (Project, error) {
	project, err := s.Project(ctx, projectID)
	if err != nil {
		return Project{}, err
	}
	var count int
	if err := s.DB.GetContext(ctx, &count, s.Q(`SELECT COUNT(*) FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.project_id=? AND w.server_id=? AND s.revoked_at IS NULL`), projectID, serverID); err != nil {
		return Project{}, err
	}
	if count == 0 {
		return Project{}, sql.ErrNoRows
	}
	var preference struct {
		Name     string     `db:"name"`
		PinnedAt *time.Time `db:"pinned_at"`
		HiddenAt *time.Time `db:"hidden_at"`
	}
	err = s.DB.GetContext(ctx, &preference, s.Q(`SELECT name,pinned_at,hidden_at FROM project_server_preferences WHERE project_id=? AND server_id=?`), projectID, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return project, nil
	}
	if err != nil {
		return Project{}, err
	}
	if preference.Name != "" {
		project.Name = preference.Name
	}
	project.PinnedAt, project.HiddenAt = preference.PinnedAt, preference.HiddenAt
	return project, nil
}

func (s *Store) UpdateProjectServerPreferences(ctx context.Context, projectID, serverID string, name *string, pinned, hidden *bool) (Project, error) {
	if name == nil && pinned == nil && hidden == nil {
		return Project{}, errors.New("at least one server project preference is required")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.GetContext(ctx, &exists, s.Q(`SELECT 1 FROM projects p WHERE p.id=? AND EXISTS (SELECT 1 FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.project_id=p.id AND w.server_id=? AND s.revoked_at IS NULL)`), projectID, serverID); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO project_server_preferences(project_id,server_id,pinned_at,hidden_at) SELECT id,?,pinned_at,hidden_at FROM projects WHERE id=? ON CONFLICT(project_id,server_id) DO NOTHING`), serverID, projectID); err != nil {
		return Project{}, err
	}
	var sets []string
	var args []any
	if name != nil {
		sets = append(sets, "name=?")
		args = append(args, *name)
	}
	for _, field := range []struct {
		key   string
		value *bool
	}{{"pinned_at", pinned}, {"hidden_at", hidden}} {
		if field.value != nil {
			var at *time.Time
			if *field.value {
				now := time.Now().UTC()
				at = &now
			}
			sets = append(sets, field.key+"=?")
			args = append(args, at)
		}
	}
	args = append(args, projectID, serverID)
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE project_server_preferences SET `+strings.Join(sets, ",")+` WHERE project_id=? AND server_id=?`), args...); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return s.ProjectOnServer(ctx, projectID, serverID)
}
