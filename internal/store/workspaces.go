package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type Workspace struct {
	ProjectDisplayName string     `db:"project_display_name" json:"project_display_name"`
	ProjectPinnedAt    *time.Time `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt    *time.Time `db:"project_hidden_at" json:"project_hidden_at"`
	ID                 string     `db:"id" json:"id"`
	ProjectID          string     `db:"project_id" json:"project_id"`
	ServerID           string     `db:"server_id" json:"server_id"`
	Path               string     `db:"path" json:"path"`
	DisplayName        string     `db:"display_name" json:"display_name"`
	ManagementMode     string     `db:"management_mode" json:"management_mode"`
	Status             string     `db:"status" json:"status"`
	Kind               string     `db:"kind" json:"kind"`
	ParentWorkspaceID  *string    `db:"parent_workspace_id" json:"parent_workspace_id"`
	Branch             string     `db:"branch" json:"branch"`
	CommitSHA          string     `db:"commit_sha" json:"commit_sha"`
	Dirty              int        `db:"dirty" json:"dirty"`
	LastGitRefreshAt   *time.Time `db:"last_git_refresh_at" json:"last_git_refresh_at"`
	GitError           string     `db:"git_error" json:"git_error"`
	LastScannedAt      time.Time  `db:"last_scanned_at" json:"last_scanned_at"`
	ServerName         string     `db:"server_name" json:"server_name"`
	ProjectName        string     `db:"project_name" json:"project_name"`
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	return s.ListWorkspacesFiltered(ctx, "", "", "")
}

var ErrInvalidWorkspaceGitStatus = errors.New("invalid workspace git status")

func (s *Store) ListWorkspacesFiltered(ctx context.Context, path, serverID, gitStatus string) ([]Workspace, error) {
	filters := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if path = strings.TrimSpace(path); path != "" {
		filters = append(filters, `w.path LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likePatternEscaper.Replace(path)+"%")
	}
	if serverID = strings.TrimSpace(serverID); serverID != "" {
		filters = append(filters, "w.server_id=?")
		args = append(args, serverID)
	}
	switch strings.TrimSpace(strings.ToLower(gitStatus)) {
	case "":
	case "clean":
		filters = append(filters, "w.dirty=0 AND w.git_error=''")
	case "dirty":
		filters = append(filters, "w.dirty<>0")
	case "error":
		filters = append(filters, "w.git_error<>''")
	default:
		return nil, ErrInvalidWorkspaceGitStatus
	}
	where := ""
	if len(filters) > 0 {
		where = " WHERE " + strings.Join(filters, " AND ")
	}
	var out []Workspace
	err := s.DB.SelectContext(ctx, &out, s.Q(`SELECT w.id,w.project_id,w.server_id,w.path,w.display_name,w.management_mode,w.status,w.kind,w.parent_workspace_id,w.branch,w.commit_sha,w.dirty,w.last_git_refresh_at,w.git_error,w.last_scanned_at,s.name server_name,p.name project_name,`+projectServerName+` AS project_display_name,ps.pinned_at AS project_pinned_at,ps.hidden_at AS project_hidden_at FROM workspaces w JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id`+projectServerJoin+where+` ORDER BY p.name,s.name`), args...)
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
	MIMEType    string     `db:"mime_type" json:"mime_type"`
	Encoding    string     `db:"encoding" json:"encoding"`
	Size        int64      `db:"size" json:"size"`
	Truncated   int        `db:"truncated" json:"truncated"`
	Status      string     `db:"status" json:"status"`
	Error       string     `db:"error" json:"error"`
	RequestedAt *time.Time `db:"requested_at" json:"requested_at"`
	UpdatedAt   *time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) WorkspaceFilePreview(ctx context.Context, workspaceID, path string) (WorkspaceFilePreview, error) {
	var preview WorkspaceFilePreview
	err := s.DB.GetContext(ctx, &preview, s.Q("SELECT workspace_id,path,content,mime_type,encoding,size,truncated,status,error,requested_at,updated_at FROM workspace_file_previews WHERE workspace_id=? AND path=?"), workspaceID, path)
	return preview, err
}

func (s *Store) BeginWorkspaceFilePreview(ctx context.Context, workspaceID, path string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO workspace_file_previews(workspace_id,path,content,size,truncated,status,error,requested_at) VALUES(?,?,'',0,0,'loading','',?) ON CONFLICT(workspace_id) DO UPDATE SET path=excluded.path,content='',mime_type='',encoding='',size=0,truncated=0,status='loading',error='',requested_at=excluded.requested_at,updated_at=NULL`), workspaceID, path, now)
	return err
}

func (s *Store) SaveWorkspaceFilePreview(ctx context.Context, workspaceID, path string, result protocol.WorkspaceFilePreviewResult) error {
	truncated := 0
	if result.Truncated {
		truncated = 1
	}
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspace_file_previews SET content=?,mime_type=?,encoding=?,size=?,truncated=?,status='succeeded',error='',updated_at=? WHERE workspace_id=? AND path=?"), result.Content, result.MIMEType, result.Encoding, result.Size, truncated, time.Now().UTC(), workspaceID, path)
	return err
}

func (s *Store) FailWorkspaceFilePreview(ctx context.Context, workspaceID, path, message string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspace_file_previews SET content='',mime_type='',encoding='',size=0,truncated=0,status='failed',error=?,updated_at=? WHERE workspace_id=? AND path=?"), message, time.Now().UTC(), workspaceID, path)
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
