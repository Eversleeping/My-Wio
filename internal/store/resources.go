package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

var ErrThreadActive = errors.New("Codex session is active")
var ErrInvalidEditTarget = errors.New("invalid Codex message edit target")
var ErrApprovalResolved = errors.New("approval was already resolved")

type Thread struct {
	ID              string     `db:"id" json:"id"`
	WorkspaceID     string     `db:"workspace_id" json:"workspace_id"`
	ProjectID       string     `db:"project_id" json:"project_id"`
	CodexThreadID   string     `db:"codex_thread_id" json:"codex_thread_id"`
	Title           string     `db:"title" json:"title"`
	Status          string     `db:"status" json:"status"`
	Path            string     `db:"path" json:"path"`
	ServerID        string     `db:"server_id" json:"server_id"`
	ServerName      string     `db:"server_name" json:"server_name"`
	ProjectName     string     `db:"project_name" json:"project_name"`
	PinnedAt        *time.Time `db:"pinned_at" json:"pinned_at"`
	ArchivedAt      *time.Time `db:"archived_at" json:"archived_at"`
	ProjectPinnedAt *time.Time `db:"project_pinned_at" json:"project_pinned_at"`
	ProjectHiddenAt *time.Time `db:"project_hidden_at" json:"project_hidden_at"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at" json:"updated_at"`
}

func (s *Store) ListThreads(ctx context.Context, archivedOption ...string) ([]Thread, error) {
	var out []Thread
	archived := ""
	if len(archivedOption) > 0 {
		archived = archivedOption[0]
	}
	filter := " WHERE t.archived_at IS NULL"
	if archived == "true" {
		filter = " WHERE t.archived_at IS NOT NULL"
	} else if archived == "all" {
		filter = ""
	}
	err := s.DB.SelectContext(ctx, &out, `SELECT t.id,t.workspace_id,w.project_id,t.codex_thread_id,t.title,t.status,w.path,w.server_id,s.name server_name,p.name project_name,t.pinned_at,t.archived_at,p.pinned_at project_pinned_at,p.hidden_at project_hidden_at,t.created_at,t.updated_at FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id`+filter+` ORDER BY CASE WHEN p.pinned_at IS NULL THEN 1 ELSE 0 END,p.pinned_at DESC,CASE WHEN t.pinned_at IS NULL THEN 1 ELSE 0 END,t.pinned_at DESC,t.updated_at DESC`)
	return out, err
}

func (s *Store) Thread(ctx context.Context, id string) (Thread, error) {
	var out Thread
	err := s.DB.GetContext(ctx, &out, s.Q(`SELECT t.id,t.workspace_id,w.project_id,t.codex_thread_id,t.title,t.status,w.path,w.server_id,s.name server_name,p.name project_name,t.pinned_at,t.archived_at,p.pinned_at project_pinned_at,p.hidden_at project_hidden_at,t.created_at,t.updated_at FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id JOIN servers s ON s.id=w.server_id JOIN projects p ON p.id=w.project_id WHERE t.id=?`), id)
	return out, err
}

func (s *Store) CreateThread(ctx context.Context, workspaceID, title string) (Thread, error) {
	if title == "" {
		title = "New session"
	}
	id := NewID()
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO codex_threads(id,workspace_id,title) VALUES(?,?,?)"), id, workspaceID, title)
	if err != nil {
		return Thread{}, err
	}
	return s.Thread(ctx, id)
}

func (s *Store) DeleteThread(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.Q("DELETE FROM codex_threads WHERE id=? AND status NOT IN ('queued','running')"), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var count int
		if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=?"), id); err != nil {
			return err
		}
		if count == 0 {
			return sql.ErrNoRows
		}
		return ErrThreadActive
	}
	if _, err := tx.ExecContext(ctx, s.Q("DELETE FROM events WHERE stream_id=?"), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetThreadStatus(ctx context.Context, id, status string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET status=?,updated_at=? WHERE id=?"), status, time.Now().UTC(), id)
	return err
}

func (s *Store) ClaimThreadForTurn(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET status='queued',updated_at=? WHERE id=? AND status NOT IN ('queued','running')"), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrThreadActive
	}
	return nil
}

func (s *Store) SetThreadTitle(ctx context.Context, id, title string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET title=?,updated_at=? WHERE id=?"), title, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateThread(ctx context.Context, id string, title *string, pinned *bool, archivedOption ...*bool) (Thread, error) {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if title != nil {
		sets = append(sets, "title=?")
		args = append(args, *title)
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
	var archived *bool
	if len(archivedOption) > 0 {
		archived = archivedOption[0]
	}
	if archived != nil {
		sets = append(sets, "archived_at=?")
		if *archived {
			args = append(args, now)
		} else {
			args = append(args, nil)
		}
	}
	if len(sets) == 0 {
		return s.Thread(ctx, id)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, now, id)
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET "+strings.Join(sets, ",")+" WHERE id=?"), args...)
	if err != nil {
		return Thread{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Thread{}, err
	}
	if rows == 0 {
		return Thread{}, sql.ErrNoRows
	}
	return s.Thread(ctx, id)
}

func (s *Store) ArchiveProjectThreads(ctx context.Context, projectID string) (int64, error) {
	now := time.Now().UTC()
	result, err := s.DB.ExecContext(ctx, s.Q(`UPDATE codex_threads SET archived_at=?,updated_at=? WHERE archived_at IS NULL AND workspace_id IN (SELECT id FROM workspaces WHERE project_id=?) AND status NOT IN ('queued','running')`), now, now, projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) CommitThreadFork(ctx context.Context, command protocol.ForkThreadCommand, codexThread string) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.GetContext(ctx, &existing, s.Q("SELECT codex_thread_id FROM codex_threads WHERE id=?"), command.TargetThreadID)
	if err == nil {
		if existing == codexThread {
			return nil
		}
		return errors.New("fork target already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=? AND workspace_id=?"), command.SourceThreadID, command.WorkspaceID); err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO codex_threads(id,workspace_id,codex_thread_id,title,status,last_sequence,created_at,updated_at) SELECT ?,workspace_id,?,?,'idle',last_sequence,?,? FROM codex_threads WHERE id=?`), command.TargetThreadID, codexThread, command.Title, now, now, command.SourceThreadID); err != nil {
		return err
	}
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &events, s.Q(`SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? ORDER BY sequence`), command.SourceThreadID); err != nil {
		return err
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)`), NewID(), command.TargetThreadID, event.Sequence, event.Kind, event.OccurredAt, event.Payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CommitGitWorktree(ctx context.Context, command protocol.GitWorktreeCreateCommand, result protocol.GitWorktreeCreateResult) error {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM workspaces WHERE id=? AND project_id=? AND server_id=(SELECT server_id FROM workspaces WHERE id=?)"), command.SourceWorkspaceID, command.ProjectID, command.SourceWorkspaceID); err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	if result.Path == "" || result.Branch != command.Branch || result.CommitSHA == "" {
		return errors.New("worktree result does not match command")
	}
	_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,parent_workspace_id,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at)
		SELECT ?,project_id,server_id,?,?,'managed','ready','worktree',?,?,?,0,?,? FROM workspaces WHERE id=?`), command.TargetWorkspaceID, result.Path, command.Branch, command.SourceWorkspaceID, result.Branch, result.CommitSHA, time.Now().UTC(), time.Now().UTC(), command.SourceWorkspaceID)
	if err != nil {
		return err
	}
	if command.TargetThreadID == "" {
		return tx.Commit()
	}
	if command.SourceThreadID == "" || result.CodexThread == "" {
		return errors.New("continued worktree has incomplete thread result")
	}
	var sourceCount int
	if err := tx.GetContext(ctx, &sourceCount, s.Q("SELECT COUNT(*) FROM codex_threads WHERE id=? AND workspace_id=?"), command.SourceThreadID, command.SourceWorkspaceID); err != nil {
		return err
	}
	if sourceCount != 1 {
		return sql.ErrNoRows
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO codex_threads(id,workspace_id,codex_thread_id,title,status,last_sequence,created_at,updated_at)
		SELECT ?,?,?,?,'idle',last_sequence,?,? FROM codex_threads WHERE id=?`), command.TargetThreadID, command.TargetWorkspaceID, result.CodexThread, command.Title, now, now, command.SourceThreadID); err != nil {
		return err
	}
	var events []struct {
		Sequence   int64     `db:"sequence"`
		Kind       string    `db:"kind"`
		OccurredAt time.Time `db:"occurred_at"`
		Payload    string    `db:"payload"`
	}
	if err := tx.SelectContext(ctx, &events, s.Q("SELECT sequence,kind,occurred_at,payload FROM events WHERE stream_id=? ORDER BY sequence"), command.SourceThreadID); err != nil {
		return err
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO events(event_id,stream_id,sequence,kind,occurred_at,payload) VALUES(?,?,?,?,?,?)"), NewID(), command.TargetThreadID, event.Sequence, event.Kind, event.OccurredAt, event.Payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetThreadTitleIfDefault(ctx context.Context, id, title string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE codex_threads SET title=?,updated_at=? WHERE id=? AND title='New session'"), title, time.Now().UTC(), id)
	return err
}

func (s *Store) ResolvePendingApprovals(ctx context.Context, threadID, decision string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE approvals SET status='resolved',decision=?,resolved_at=? WHERE thread_id=? AND status='pending'"), decision, time.Now().UTC(), threadID)
	return err
}

func (s *Store) ResolveApprovalAndQueue(ctx context.Context, approvalID, serverID, decision string, command protocol.ApprovalDecisionCommand) (string, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.Q("UPDATE approvals SET status='resolved',decision=?,resolved_at=? WHERE id=? AND status='pending'"), decision, now, approvalID)
	if err != nil {
		return "", err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if rows != 1 {
		return "", ErrApprovalResolved
	}
	operationID := NewID()
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?)"), operationID, serverID, "codex.approval", string(payload), "approval:"+approvalID, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
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

type SecretSet struct {
	ID        string    `db:"id" json:"id"`
	Name      string    `db:"name" json:"name"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

func (s *Store) ListSecretSets(ctx context.Context) ([]SecretSet, error) {
	var out []SecretSet
	err := s.DB.SelectContext(ctx, &out, "SELECT id,name,updated_at FROM secret_sets ORDER BY name")
	return out, err
}

func (s *Store) UpsertSecretSet(ctx context.Context, id, name, ciphertext string) (string, error) {
	if id == "" {
		id = NewID()
	}
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO secret_sets(id,name,ciphertext) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET ciphertext=excluded.ciphertext,updated_at=?`), id, name, ciphertext, time.Now().UTC())
	if err != nil {
		return "", err
	}
	if err := s.DB.GetContext(ctx, &id, s.Q("SELECT id FROM secret_sets WHERE name=?"), name); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) SecretCiphertext(ctx context.Context, id string) (string, error) {
	var value string
	err := s.DB.GetContext(ctx, &value, s.Q("SELECT ciphertext FROM secret_sets WHERE id=?"), id)
	return value, err
}

type DeploymentTarget struct {
	ID                   string     `db:"id" json:"id"`
	ProjectID            string     `db:"project_id" json:"project_id"`
	ServerID             string     `db:"server_id" json:"server_id"`
	SourceType           string     `db:"source_type" json:"source_type"`
	WorkspaceID          string     `db:"workspace_id" json:"workspace_id"`
	SecretSetID          string     `db:"secret_set_id" json:"secret_set_id"`
	Environment          string     `db:"environment" json:"environment"`
	Repository           string     `db:"repository" json:"repository"`
	GitRef               string     `db:"git_ref" json:"git_ref"`
	ComposeFile          string     `db:"compose_file" json:"compose_file"`
	WorkingDir           string     `db:"working_dir" json:"working_dir"`
	BuildMode            string     `db:"build_mode" json:"build_mode"`
	HealthChecks         string     `db:"health_checks" json:"health_checks"`
	ReleaseRoot          string     `db:"release_root" json:"release_root"`
	PublicURL            string     `db:"public_url" json:"public_url"`
	ProjectName          string     `db:"project_name" json:"project_name"`
	ServerName           string     `db:"server_name" json:"server_name"`
	WorkspacePath        string     `db:"workspace_path" json:"workspace_path"`
	WorkspaceName        string     `db:"workspace_name" json:"workspace_name"`
	ContainerOperationID string     `db:"container_operation_id" json:"container_operation_id"`
	ContainerAction      string     `db:"container_action" json:"container_action"`
	ContainerStatus      string     `db:"container_status" json:"container_status"`
	ContainerMessage     string     `db:"container_message" json:"container_message"`
	ContainerUpdatedAt   *time.Time `db:"container_updated_at" json:"container_updated_at"`
}

const deploymentTargetSelect = `SELECT t.id,t.project_id,t.server_id,t.source_type,COALESCE(t.workspace_id,'') workspace_id,COALESCE(t.secret_set_id,'') secret_set_id,t.environment,t.repository,t.git_ref,t.compose_file,t.working_dir,t.build_mode,t.health_checks,t.release_root,t.public_url,p.name project_name,s.name server_name,COALESCE(w.path,'') workspace_path,COALESCE(w.display_name,'') workspace_name,COALESCE(cs.operation_id,'') container_operation_id,COALESCE(cs.action,'') container_action,COALESCE(cs.status,'unknown') container_status,COALESCE(cs.message,'') container_message,cs.updated_at container_updated_at FROM deployment_targets t JOIN projects p ON p.id=t.project_id JOIN servers s ON s.id=t.server_id LEFT JOIN workspaces w ON w.id=t.workspace_id LEFT JOIN deployment_container_state cs ON cs.target_id=t.id`

func (s *Store) ListDeploymentTargets(ctx context.Context) ([]DeploymentTarget, error) {
	var out []DeploymentTarget
	err := s.DB.SelectContext(ctx, &out, deploymentTargetSelect+` ORDER BY p.name,t.environment`)
	return out, err
}

func (s *Store) DeploymentTarget(ctx context.Context, id string) (DeploymentTarget, error) {
	var out DeploymentTarget
	err := s.DB.GetContext(ctx, &out, s.Q(deploymentTargetSelect+` WHERE t.id=?`), id)
	return out, err
}

func (s *Store) CreateDeploymentTarget(ctx context.Context, target DeploymentTarget) (DeploymentTarget, error) {
	target.ID = NewID()
	if target.GitRef == "" {
		target.GitRef = "main"
	}
	if target.SourceType == "" {
		target.SourceType = "remote"
	}
	if target.ComposeFile == "" {
		target.ComposeFile = "compose.yaml"
	}
	if target.BuildMode == "" {
		target.BuildMode = "build"
	}
	if target.ReleaseRoot == "" {
		target.ReleaseRoot = "/var/lib/wio-agent/releases"
	}
	if target.HealthChecks == "" {
		target.HealthChecks = "[]"
	}
	var secret any = target.SecretSetID
	if target.SecretSetID == "" {
		secret = nil
	}
	var workspace any = target.WorkspaceID
	if target.WorkspaceID == "" {
		workspace = nil
	}
	_, err := s.DB.ExecContext(ctx, s.Q(`INSERT INTO deployment_targets(id,project_id,server_id,source_type,workspace_id,secret_set_id,environment,repository,git_ref,compose_file,working_dir,build_mode,health_checks,release_root,public_url) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), target.ID, target.ProjectID, target.ServerID, target.SourceType, workspace, secret, target.Environment, target.Repository, target.GitRef, target.ComposeFile, target.WorkingDir, target.BuildMode, target.HealthChecks, target.ReleaseRoot, target.PublicURL)
	if err != nil {
		return DeploymentTarget{}, err
	}
	return s.DeploymentTarget(ctx, target.ID)
}

func (s *Store) UpdateDeploymentTarget(ctx context.Context, target DeploymentTarget) (DeploymentTarget, error) {
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return DeploymentTarget{}, err
	}
	defer tx.Rollback()
	var active int
	if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), target.ID); err != nil {
		return DeploymentTarget{}, err
	}
	if active > 0 {
		return DeploymentTarget{}, ErrDeploymentActive
	}
	if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), target.ID); err != nil {
		return DeploymentTarget{}, err
	}
	if active > 0 {
		return DeploymentTarget{}, ErrDeploymentContainerActive
	}
	var secret any = target.SecretSetID
	if target.SecretSetID == "" {
		secret = nil
	}
	var workspace any = target.WorkspaceID
	if target.WorkspaceID == "" {
		workspace = nil
	}
	result, err := tx.ExecContext(ctx, s.Q(`UPDATE deployment_targets SET project_id=?,server_id=?,source_type=?,workspace_id=?,secret_set_id=?,environment=?,repository=?,git_ref=?,compose_file=?,working_dir=?,build_mode=?,health_checks=?,release_root=?,public_url=? WHERE id=?`), target.ProjectID, target.ServerID, target.SourceType, workspace, secret, target.Environment, target.Repository, target.GitRef, target.ComposeFile, target.WorkingDir, target.BuildMode, target.HealthChecks, target.ReleaseRoot, target.PublicURL, target.ID)
	if err != nil {
		return DeploymentTarget{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return DeploymentTarget{}, err
	}
	if rows == 0 {
		return DeploymentTarget{}, sql.ErrNoRows
	}
	var updated DeploymentTarget
	if err := tx.GetContext(ctx, &updated, s.Q(deploymentTargetSelect+" WHERE t.id=?"), target.ID); err != nil {
		return DeploymentTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentTarget{}, err
	}
	return updated, nil
}

func (s *Store) DeleteDeploymentTarget(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')`), id); err != nil {
		return err
	}
	if active > 0 {
		return ErrDeploymentActive
	}
	if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), id); err != nil {
		return err
	}
	if active > 0 {
		return ErrDeploymentContainerActive
	}
	result, err := tx.ExecContext(ctx, s.Q("DELETE FROM deployment_targets WHERE id=?"), id)
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
	return tx.Commit()
}

type Deployment struct {
	ID             string     `db:"id" json:"id"`
	TargetID       string     `db:"target_id" json:"target_id"`
	OperationID    string     `db:"operation_id" json:"operation_id"`
	CommitRef      string     `db:"commit_ref" json:"commit_ref"`
	ResolvedCommit string     `db:"resolved_commit" json:"resolved_commit"`
	Status         string     `db:"status" json:"status"`
	Message        string     `db:"message" json:"message"`
	ProjectName    string     `db:"project_name" json:"project_name"`
	Environment    string     `db:"environment" json:"environment"`
	PublicURL      string     `db:"public_url" json:"public_url"`
	CreatedAt      time.Time  `db:"created_at" json:"created_at"`
	StartedAt      *time.Time `db:"started_at" json:"started_at"`
	FinishedAt     *time.Time `db:"finished_at" json:"finished_at"`
}

var ErrDeploymentActive = errors.New("deployment is active")
var ErrDeploymentContainerActive = errors.New("container operation is active")

type DeploymentEvent struct {
	ID           string    `db:"id" json:"id"`
	DeploymentID string    `db:"deployment_id" json:"deployment_id"`
	Status       string    `db:"status" json:"status"`
	Message      string    `db:"message" json:"message"`
	Content      string    `db:"content" json:"content"`
	OccurredAt   time.Time `db:"occurred_at" json:"occurred_at"`
}

func (s *Store) ListDeployments(ctx context.Context) ([]Deployment, error) {
	var out []Deployment
	err := s.DB.SelectContext(ctx, &out, `SELECT d.id,d.target_id,COALESCE(d.operation_id,'') operation_id,d.commit_ref,d.resolved_commit,d.status,d.message,p.name project_name,t.environment,t.public_url,d.created_at,d.started_at,d.finished_at FROM deployments d JOIN deployment_targets t ON t.id=d.target_id JOIN projects p ON p.id=t.project_id ORDER BY d.created_at DESC LIMIT 200`)
	return out, err
}

func (s *Store) CreateDeployment(ctx context.Context, targetID, commitRef string) (Deployment, error) {
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback()
	var active int
	if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), targetID); err != nil {
		return Deployment{}, err
	}
	if active > 0 {
		return Deployment{}, ErrDeploymentActive
	}
	if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), targetID); err != nil {
		return Deployment{}, err
	}
	if active > 0 {
		return Deployment{}, ErrDeploymentContainerActive
	}
	id := NewID()
	_, err = tx.ExecContext(ctx, s.Q("INSERT INTO deployments(id,target_id,commit_ref) VALUES(?,?,?)"), id, targetID, commitRef)
	if err != nil {
		return Deployment{}, err
	}
	action := "deploy"
	message := "deployment queued"
	if commitRef == "rollback" {
		action = "rollback"
		message = "rollback queued"
	}
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_container_state(target_id,operation_id,action,status,message,content,updated_at)
		VALUES(?,NULL,?,'pending',?,'',?)
		ON CONFLICT(target_id) DO UPDATE SET operation_id=NULL,action=excluded.action,status='pending',message=excluded.message,content='',updated_at=excluded.updated_at`), targetID, action, message, time.Now().UTC()); err != nil {
		return Deployment{}, err
	}
	var deployment Deployment
	err = tx.GetContext(ctx, &deployment, s.Q(`SELECT d.id,d.target_id,COALESCE(d.operation_id,'') operation_id,d.commit_ref,d.resolved_commit,d.status,d.message,p.name project_name,t.environment,t.public_url,d.created_at,d.started_at,d.finished_at FROM deployments d JOIN deployment_targets t ON t.id=d.target_id JOIN projects p ON p.id=t.project_id WHERE d.id=?`), id)
	if err != nil {
		return Deployment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

func (s *Store) AttachDeploymentOperation(ctx context.Context, deploymentID, operationID string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE deployments SET operation_id=? WHERE id=?"), operationID, deploymentID)
	return err
}

func (s *Store) Deployment(ctx context.Context, id string) (Deployment, error) {
	var deployment Deployment
	err := s.DB.GetContext(ctx, &deployment, s.Q(`SELECT d.id,d.target_id,COALESCE(d.operation_id,'') operation_id,d.commit_ref,d.resolved_commit,d.status,d.message,p.name project_name,t.environment,t.public_url,d.created_at,d.started_at,d.finished_at FROM deployments d JOIN deployment_targets t ON t.id=d.target_id JOIN projects p ON p.id=t.project_id WHERE d.id=?`), id)
	return deployment, err
}

func (s *Store) DeploymentEvents(ctx context.Context, deploymentID string) ([]DeploymentEvent, error) {
	var events []DeploymentEvent
	err := s.DB.SelectContext(ctx, &events, s.Q("SELECT id,deployment_id,status,message,content,occurred_at FROM deployment_events WHERE deployment_id=? ORDER BY occurred_at,id"), deploymentID)
	return events, err
}

func (s *Store) AddDeploymentEvent(ctx context.Context, event DeploymentEvent) error {
	if event.ID == "" {
		event.ID = NewID()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO deployment_events(id,deployment_id,status,message,content,occurred_at) VALUES(?,?,?,?,?,?)"), event.ID, event.DeploymentID, event.Status, event.Message, event.Content, event.OccurredAt)
	return err
}

func (s *Store) SaveDeploymentStatus(ctx context.Context, update protocol.DeploymentStatus) error {
	switch update.Status {
	case "queued", "preparing", "running", "succeeded", "failed", "rolled_back", "canceled":
	default:
		return fmt.Errorf("invalid deployment status %q", update.Status)
	}
	if len(update.Message) > 8192 {
		update.Message = update.Message[:8192] + "..."
	}
	if len(update.Content) > 1<<20 {
		update.Content = update.Content[:1<<20] + "\n... log truncated ..."
	}
	update.Message = strings.ToValidUTF8(update.Message, "?")
	update.Content = strings.ToValidUTF8(update.Content, "?")
	now := time.Now().UTC()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var metadata struct {
		TargetID  string `db:"target_id"`
		CommitRef string `db:"commit_ref"`
	}
	if err := tx.GetContext(ctx, &metadata, s.Q("SELECT target_id,commit_ref FROM deployments WHERE id=?"), update.DeploymentID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.Q("UPDATE deployments SET status=?,message=?,resolved_commit=CASE WHEN ?='' THEN resolved_commit ELSE ? END,started_at=CASE WHEN ? IN ('preparing','running') AND started_at IS NULL THEN ? ELSE started_at END,finished_at=CASE WHEN ? IN ('succeeded','failed','rolled_back','canceled') THEN ? ELSE finished_at END WHERE id=?"), update.Status, update.Message, update.ResolvedCommit, update.ResolvedCommit, update.Status, now, update.Status, now, update.DeploymentID)
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
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO deployment_events(id,deployment_id,status,message,content,occurred_at) VALUES(?,?,?,?,?,?)"), NewID(), update.DeploymentID, update.Status, update.Message, update.Content, now); err != nil {
		return err
	}
	action := "deploy"
	if metadata.CommitRef == "rollback" || update.Status == "rolled_back" {
		action = "rollback"
	}
	containerStatus := ""
	switch update.Status {
	case "queued", "preparing", "running":
		containerStatus = "pending"
	case "succeeded", "rolled_back":
		containerStatus = "running"
	case "failed", "canceled":
		containerStatus = "unknown"
	}
	if containerStatus != "" {
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_container_state(target_id,operation_id,action,status,message,content,updated_at)
			VALUES(?,NULL,?,?,?,?,?)
			ON CONFLICT(target_id) DO UPDATE SET operation_id=NULL,action=excluded.action,status=excluded.status,message=excluded.message,content=excluded.content,updated_at=excluded.updated_at`), metadata.TargetID, action, containerStatus, update.Message, update.Content, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteDeployment(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, s.Q(`DELETE FROM deployments WHERE id=? AND status NOT IN ('queued','preparing','running')`), id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	var exists int
	if err := s.DB.GetContext(ctx, &exists, s.Q("SELECT COUNT(*) FROM deployments WHERE id=?"), id); err != nil {
		return err
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	return ErrDeploymentActive
}

// QueueDeploymentContainerOperation stores an encrypted Compose lifecycle
// command and marks the target as pending in one transaction. This prevents a
// second lifecycle action (or a deployment) from racing the first one.
func (s *Store) QueueDeploymentContainerOperation(ctx context.Context, targetID, serverID, action, ciphertext, idempotency string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("encrypted operation payload must use a supported Vault format")
	}
	switch action {
	case "start", "stop", "restart", "remove":
	default:
		return "", fmt.Errorf("unsupported container action %q", action)
	}
	if strings.TrimSpace(idempotency) == "" {
		return "", errors.New("container operation idempotency key is required")
	}
	tx, err := s.DB.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var existing string
	err = tx.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency)
	if err == nil {
		if commitErr := tx.Commit(); commitErr != nil {
			return "", commitErr
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var targetServer string
	if err := tx.GetContext(ctx, &targetServer, s.Q("SELECT server_id FROM deployment_targets WHERE id=?"), targetID); err != nil {
		return "", err
	}
	if targetServer != serverID {
		return "", errors.New("deployment target does not belong to the requested server")
	}
	var activeDeployments int
	if err := tx.GetContext(ctx, &activeDeployments, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), targetID); err != nil {
		return "", err
	}
	if activeDeployments > 0 {
		return "", ErrDeploymentActive
	}
	var activeContainer int
	if err := tx.GetContext(ctx, &activeContainer, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), targetID); err != nil {
		return "", err
	}
	if activeContainer > 0 {
		return "", ErrDeploymentContainerActive
	}
	operationID := NewID()
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,?,?,?)"), operationID, serverID, "deploy.container", ciphertext, idempotency, now); err != nil {
		if lookupErr := tx.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); lookupErr == nil {
			_ = tx.Commit()
			return existing, nil
		}
		return "", err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_container_state(target_id,operation_id,action,status,message,content,updated_at)
		VALUES(?,?,?,'pending',?,'',?)
		ON CONFLICT(target_id) DO UPDATE SET operation_id=excluded.operation_id,action=excluded.action,status='pending',message=excluded.message,content='',updated_at=excluded.updated_at`), targetID, operationID, action, "container "+action+" queued", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
}

// CompleteDeploymentContainerOperation projects the Agent result onto the
// target's latest runtime state. Unknown operation IDs are ignored so this is
// safe for operations created before runtime state tracking was introduced.
func (s *Store) CompleteDeploymentContainerOperation(ctx context.Context, operationID string, result protocol.OperationResult) error {
	if operationID == "" {
		return nil
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state struct {
		TargetID string `db:"target_id"`
		Action   string `db:"action"`
	}
	if err := tx.GetContext(ctx, &state, s.Q("SELECT target_id,action FROM deployment_container_state WHERE operation_id=?"), operationID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	status := "failed"
	message := strings.TrimSpace(result.Message)
	content := ""
	resultMismatch := ""
	if len(result.Data) > 0 {
		var actionResult protocol.ContainerActionResult
		if json.Unmarshal(result.Data, &actionResult) == nil {
			if actionResult.TargetID != "" && actionResult.TargetID != state.TargetID {
				resultMismatch = "container action result target mismatch"
			}
			if actionResult.Action != "" && actionResult.Action != state.Action {
				resultMismatch = "container action result action mismatch"
			}
			if actionResult.Message != "" {
				message = actionResult.Message
			}
			content = actionResult.Content
		}
	}
	if resultMismatch != "" {
		message = resultMismatch
		content = ""
	} else if result.Status == "succeeded" {
		switch state.Action {
		case "start", "restart":
			status = "running"
		case "stop":
			status = "stopped"
		case "remove":
			status = "removed"
		default:
			return fmt.Errorf("unsupported stored container action %q", state.Action)
		}
	}
	message = truncateText(strings.ToValidUTF8(message, "?"), 8192)
	content = truncateText(strings.ToValidUTF8(content, "?"), 1<<20)
	if _, err := tx.ExecContext(ctx, s.Q("UPDATE deployment_container_state SET status=?,message=?,content=?,updated_at=? WHERE operation_id=?"), status, message, content, time.Now().UTC(), operationID); err != nil {
		return err
	}
	return tx.Commit()
}

func truncateText(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[:size] + "..."
}

type Alert struct {
	ID             string     `db:"id" json:"id"`
	ServerID       string     `db:"server_id" json:"server_id"`
	Kind           string     `db:"kind" json:"kind"`
	Severity       string     `db:"severity" json:"severity"`
	Title          string     `db:"title" json:"title"`
	Detail         string     `db:"detail" json:"detail"`
	Status         string     `db:"status" json:"status"`
	ServerName     string     `db:"server_name" json:"server_name"`
	OpenedAt       time.Time  `db:"opened_at" json:"opened_at"`
	ResolvedAt     *time.Time `db:"resolved_at" json:"resolved_at"`
	AcknowledgedAt *time.Time `db:"acknowledged_at" json:"acknowledged_at"`
}

func (s *Store) ListAlerts(ctx context.Context) ([]Alert, error) {
	var out []Alert
	err := s.DB.SelectContext(ctx, &out, `SELECT a.id,COALESCE(a.server_id,'') server_id,a.kind,a.severity,a.title,a.detail,a.status,COALESCE(s.name,'') server_name,a.opened_at,a.resolved_at,a.acknowledged_at FROM alerts a LEFT JOIN servers s ON s.id=a.server_id ORDER BY CASE a.status WHEN 'open' THEN 0 ELSE 1 END,a.opened_at DESC LIMIT 200`)
	return out, err
}

type MetricPoint struct {
	ServerID      string    `db:"server_id" json:"server_id"`
	BucketAt      time.Time `db:"bucket_at" json:"bucket_at"`
	CPUPercent    float64   `db:"cpu_percent" json:"cpu_percent"`
	MemoryPercent float64   `db:"memory_percent" json:"memory_percent"`
	DiskPercent   float64   `db:"disk_percent" json:"disk_percent"`
	Load1         float64   `db:"load_1" json:"load_1"`
	NetRxBytes    uint64    `db:"net_rx_bytes" json:"net_rx_bytes"`
	NetTxBytes    uint64    `db:"net_tx_bytes" json:"net_tx_bytes"`
}

func (s *Store) Metrics(ctx context.Context, serverID string, since time.Time) ([]MetricPoint, error) {
	var out []MetricPoint
	err := s.DB.SelectContext(ctx, &out, s.Q(`SELECT server_id,bucket_at,cpu_percent,memory_percent,disk_percent,load_1,net_rx_bytes,net_tx_bytes FROM metric_rollups WHERE server_id=? AND bucket_at>=? ORDER BY bucket_at`), serverID, since)
	return out, err
}

func (s *Store) Audit(ctx context.Context, userID, action, resourceType, resourceID string, detail any, ip string) error {
	raw, _ := json.Marshal(detail)
	_, err := s.DB.ExecContext(ctx, s.Q("INSERT INTO audit_log(id,user_id,action,resource_type,resource_id,detail,ip_address) VALUES(?,?,?,?,?,?,?)"), NewID(), userID, action, resourceType, resourceID, string(raw), ip)
	return err
}
