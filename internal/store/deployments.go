package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

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
	ConfiguredPublicURL  string     `db:"configured_public_url" json:"configured_public_url"`
	DetectedPublicURL    string     `db:"detected_public_url" json:"detected_public_url"`
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

const deploymentTargetSelect = `SELECT t.id,t.project_id,t.server_id,t.source_type,COALESCE(t.workspace_id,'') workspace_id,COALESCE(t.secret_set_id,'') secret_set_id,t.environment,t.repository,t.git_ref,t.compose_file,t.working_dir,t.build_mode,t.health_checks,t.release_root,COALESCE(NULLIF(t.public_url,''),t.detected_public_url) public_url,t.public_url configured_public_url,t.detected_public_url,p.name project_name,s.name server_name,COALESCE(w.path,'') workspace_path,COALESCE(w.display_name,'') workspace_name,COALESCE(cs.operation_id,'') container_operation_id,COALESCE(cs.action,'') container_action,COALESCE(cs.status,'unknown') container_status,COALESCE(cs.message,'') container_message,cs.updated_at container_updated_at FROM deployment_targets t JOIN projects p ON p.id=t.project_id JOIN servers s ON s.id=t.server_id LEFT JOIN workspaces w ON w.id=t.workspace_id LEFT JOIN deployment_container_state cs ON cs.target_id=t.id`

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
	var updated DeploymentTarget
	err := s.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
		var active int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), target.ID); err != nil {
			return err
		}
		if active > 0 {
			return ErrDeploymentActive
		}
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), target.ID); err != nil {
			return err
		}
		if active > 0 {
			return ErrDeploymentContainerActive
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
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		if err := tx.GetContext(ctx, &updated, s.Q(deploymentTargetSelect+" WHERE t.id=?"), target.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return DeploymentTarget{}, err
	}
	return updated, nil
}

func (s *Store) DeleteDeploymentTarget(ctx context.Context, id string) error {
	return s.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
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
		return nil
	})
}

type Deployment struct {
	ID                string              `db:"id" json:"id"`
	TargetID          string              `db:"target_id" json:"target_id"`
	OperationID       string              `db:"operation_id" json:"operation_id"`
	CommitRef         string              `db:"commit_ref" json:"commit_ref"`
	ResolvedCommit    string              `db:"resolved_commit" json:"resolved_commit"`
	Status            string              `db:"status" json:"status"`
	Message           string              `db:"message" json:"message"`
	ProjectName       string              `db:"project_name" json:"project_name"`
	Environment       string              `db:"environment" json:"environment"`
	PublicURL         string              `db:"public_url" json:"public_url"`
	SnapshotAvailable bool                `db:"snapshot_available" json:"snapshot_available"`
	SnapshotError     string              `db:"-" json:"snapshot_error,omitempty"`
	Snapshot          *DeploymentSnapshot `db:"-" json:"snapshot,omitempty"`
	CreatedAt         time.Time           `db:"created_at" json:"created_at"`
	StartedAt         *time.Time          `db:"started_at" json:"started_at"`
	FinishedAt        *time.Time          `db:"finished_at" json:"finished_at"`
}

var ErrDeploymentActive = errors.New("deployment is active")

var ErrDeploymentContainerActive = errors.New("container operation is active")

var ErrDeploymentSnapshotUnavailable = errors.New("deployment configuration snapshot is unavailable")

type DeploymentConfig struct {
	ProjectID           string     `db:"project_id" json:"project_id"`
	ProjectName         string     `db:"project_name" json:"project_name"`
	SourceType          string     `db:"source_type" json:"source_type"`
	Environment         string     `db:"environment" json:"environment"`
	ServerID            string     `db:"server_id" json:"server_id"`
	ServerName          string     `db:"server_name" json:"server_name"`
	WorkspaceID         string     `db:"workspace_id" json:"workspace_id"`
	WorkspacePath       string     `db:"workspace_path" json:"workspace_path"`
	WorkspaceName       string     `db:"workspace_name" json:"workspace_name"`
	Repository          string     `db:"repository" json:"repository"`
	GitRef              string     `db:"git_ref" json:"git_ref"`
	ResolvedCommit      string     `db:"resolved_commit" json:"resolved_commit"`
	ComposeFile         string     `db:"compose_file" json:"compose_file"`
	WorkingDir          string     `db:"working_dir" json:"working_dir"`
	BuildMode           string     `db:"build_mode" json:"build_mode"`
	ReleaseRoot         string     `db:"release_root" json:"release_root"`
	ConfiguredPublicURL string     `db:"configured_public_url" json:"configured_public_url"`
	DetectedPublicURL   string     `db:"detected_public_url" json:"detected_public_url"`
	HealthChecks        string     `db:"health_checks" json:"health_checks"`
	SecretSetID         string     `db:"secret_set_id" json:"secret_set_id"`
	SecretSetName       string     `db:"secret_set_name" json:"secret_set_name"`
	SecretSetKeyVersion int        `db:"secret_set_key_version" json:"secret_set_key_version"`
	SecretSetUpdatedAt  *time.Time `db:"secret_set_updated_at" json:"secret_set_updated_at"`
}

type DeploymentSnapshot struct {
	DeploymentID           string `db:"deployment_id" json:"deployment_id"`
	TargetID               string `db:"target_id" json:"target_id"`
	RollbackOfDeploymentID string `db:"rollback_of_deployment_id" json:"rollback_of_deployment_id,omitempty"`
	DeploymentConfig
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type DeploymentConfigChange struct {
	Field    string `json:"field"`
	Previous string `json:"previous"`
	Current  string `json:"current"`
}

type DeploymentTargetReview struct {
	TargetID          string                   `json:"target_id"`
	Current           DeploymentConfig         `json:"current"`
	LastSuccessful    *DeploymentSnapshot      `json:"last_successful,omitempty"`
	Changes           []DeploymentConfigChange `json:"changes"`
	SnapshotAvailable bool                     `json:"snapshot_available"`
}

type DeploymentEvent struct {
	ID           string    `db:"id" json:"id"`
	DeploymentID string    `db:"deployment_id" json:"deployment_id"`
	Status       string    `db:"status" json:"status"`
	Message      string    `db:"message" json:"message"`
	Content      string    `db:"content" json:"content"`
	OccurredAt   time.Time `db:"occurred_at" json:"occurred_at"`
}

const deploymentHistorySelect = `SELECT d.id,d.target_id,COALESCE(d.operation_id,'') operation_id,d.commit_ref,d.resolved_commit,d.status,d.message,
	COALESCE(ds.project_name,'') project_name,
	COALESCE(ds.environment,'') environment,
	CASE WHEN ds.deployment_id IS NULL THEN '' ELSE COALESCE(NULLIF(ds.configured_public_url,''),ds.detected_public_url) END public_url,
	CASE WHEN ds.deployment_id IS NULL THEN 0 ELSE 1 END snapshot_available,
	d.created_at,d.started_at,d.finished_at
	FROM deployments d
	JOIN deployment_targets t ON t.id=d.target_id
	JOIN projects p ON p.id=t.project_id
	LEFT JOIN deployment_config_snapshots ds ON ds.deployment_id=d.id`

func (s *Store) ListDeployments(ctx context.Context) ([]Deployment, error) {
	var out []Deployment
	err := s.DB.SelectContext(ctx, &out, deploymentHistorySelect+` ORDER BY d.created_at DESC LIMIT 200`)
	return out, err
}

func (s *Store) CreateDeployment(ctx context.Context, targetID, commitRef string) (Deployment, error) {
	var deployment Deployment
	err := s.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
		var active int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), targetID); err != nil {
			return err
		}
		if active > 0 {
			return ErrDeploymentActive
		}
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), targetID); err != nil {
			return err
		}
		if active > 0 {
			return ErrDeploymentContainerActive
		}
		id := NewID()
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO deployments(id,target_id,commit_ref) VALUES(?,?,?)"), id, targetID, commitRef); err != nil {
			return err
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_config_snapshots(
			deployment_id,target_id,project_id,project_name,source_type,environment,server_id,server_name,workspace_id,workspace_path,workspace_name,
			repository,git_ref,resolved_commit,compose_file,working_dir,build_mode,release_root,configured_public_url,detected_public_url,
			health_checks,secret_set_id,secret_set_name,secret_set_key_version,secret_set_updated_at,created_at)
			SELECT d.id,d.target_id,t.project_id,p.name,t.source_type,t.environment,t.server_id,s.name,COALESCE(t.workspace_id,''),COALESCE(w.path,''),COALESCE(w.display_name,''),
				t.repository,?,d.resolved_commit,t.compose_file,t.working_dir,t.build_mode,t.release_root,t.public_url,t.detected_public_url,
				t.health_checks,COALESCE(t.secret_set_id,''),COALESCE(ss.name,''),COALESCE(ss.key_version,0),ss.updated_at,?
			FROM deployments d
			JOIN deployment_targets t ON t.id=d.target_id
			JOIN projects p ON p.id=t.project_id
			JOIN servers s ON s.id=t.server_id
			LEFT JOIN workspaces w ON w.id=t.workspace_id
			LEFT JOIN secret_sets ss ON ss.id=t.secret_set_id
			WHERE d.id=?`), commitRef, now, id); err != nil {
			return err
		}
		action := "deploy"
		message := "deployment queued"
		if commitRef == "rollback" {
			action = "rollback"
			message = "rollback queued"
		}
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_container_state(target_id,operation_id,action,status,message,content,updated_at)
			VALUES(?,NULL,?,'pending',?,'',?)
			ON CONFLICT(target_id) DO UPDATE SET operation_id=NULL,action=excluded.action,status='pending',message=excluded.message,content='',updated_at=excluded.updated_at`), targetID, action, message, now); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &deployment, s.Q(deploymentHistorySelect+` WHERE d.id=?`), id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Deployment{}, err
	}
	return deployment, nil
}

func (s *Store) CreateRollbackDeployment(ctx context.Context, targetID, sourceDeploymentID string) (Deployment, error) {
	var id string
	err := s.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
		var active int
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), targetID); err != nil {
			return err
		}
		if active > 0 {
			return ErrDeploymentActive
		}
		if err := tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), targetID); err != nil {
			return err
		}
		if active > 0 {
			return ErrDeploymentContainerActive
		}
		var sourceStatus string
		if err := tx.GetContext(ctx, &sourceStatus, s.Q(`SELECT d.status FROM deployments d JOIN deployment_config_snapshots ds ON ds.deployment_id=d.id WHERE d.id=? AND d.target_id=?`), sourceDeploymentID, targetID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDeploymentSnapshotUnavailable
			}
			return err
		}
		if sourceStatus != "succeeded" && sourceStatus != "rolled_back" {
			return ErrDeploymentSnapshotUnavailable
		}
		id = NewID()
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO deployments(id,target_id,commit_ref) VALUES(?,?,?)"), id, targetID, "rollback"); err != nil {
			return err
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_container_state(target_id,operation_id,action,status,message,content,updated_at)
			VALUES(?,NULL,'rollback','pending','rollback queued','',?)
			ON CONFLICT(target_id) DO UPDATE SET operation_id=NULL,action='rollback',status='pending',message='rollback queued',content='',updated_at=excluded.updated_at`), targetID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_config_snapshots(
			deployment_id,target_id,project_id,project_name,source_type,environment,server_id,server_name,workspace_id,workspace_path,workspace_name,
			repository,git_ref,resolved_commit,compose_file,working_dir,build_mode,release_root,configured_public_url,detected_public_url,
			health_checks,secret_set_id,secret_set_name,secret_set_key_version,secret_set_updated_at,rollback_of_deployment_id,created_at)
			SELECT ?,?,project_id,project_name,source_type,environment,server_id,server_name,workspace_id,workspace_path,workspace_name,
				repository,git_ref,resolved_commit,compose_file,working_dir,build_mode,release_root,configured_public_url,detected_public_url,
				health_checks,secret_set_id,secret_set_name,secret_set_key_version,secret_set_updated_at,?,?
			FROM deployment_config_snapshots WHERE deployment_id=?`), id, targetID, sourceDeploymentID, now, sourceDeploymentID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Deployment{}, err
	}
	return s.Deployment(ctx, id)
}

func (s *Store) AttachDeploymentOperation(ctx context.Context, deploymentID, operationID string) error {
	_, err := s.DB.ExecContext(ctx, s.Q("UPDATE deployments SET operation_id=? WHERE id=?"), operationID, deploymentID)
	return err
}

func (s *Store) Deployment(ctx context.Context, id string) (Deployment, error) {
	var deployment Deployment
	err := s.DB.GetContext(ctx, &deployment, s.Q(deploymentHistorySelect+` WHERE d.id=?`), id)
	return deployment, err
}

const deploymentSnapshotSelect = `SELECT deployment_id,target_id,project_id,project_name,source_type,environment,server_id,server_name,workspace_id,workspace_path,workspace_name,repository,git_ref,resolved_commit,compose_file,working_dir,build_mode,release_root,configured_public_url,detected_public_url,health_checks,secret_set_id,secret_set_name,secret_set_key_version,secret_set_updated_at,COALESCE(rollback_of_deployment_id,'') rollback_of_deployment_id,created_at FROM deployment_config_snapshots ds`

func (s *Store) DeploymentSnapshot(ctx context.Context, deploymentID string) (DeploymentSnapshot, error) {
	var snapshot DeploymentSnapshot
	err := s.DB.GetContext(ctx, &snapshot, s.Q(deploymentSnapshotSelect+` WHERE deployment_id=?`), deploymentID)
	return snapshot, err
}

func (s *Store) LatestDeploymentSnapshot(ctx context.Context, targetID string) (DeploymentSnapshot, error) {
	var snapshot DeploymentSnapshot
	err := s.DB.GetContext(ctx, &snapshot, s.Q(deploymentSnapshotSelect+` WHERE deployment_id IN (SELECT ds2.deployment_id FROM deployment_config_snapshots ds2 JOIN deployments d2 ON d2.id=ds2.deployment_id WHERE ds2.target_id=? AND d2.status IN ('succeeded','rolled_back')) ORDER BY created_at DESC LIMIT 1`), targetID)
	return snapshot, err
}

func (s *Store) DeploymentTargetReview(ctx context.Context, targetID string) (DeploymentTargetReview, error) {
	_, err := s.DeploymentTarget(ctx, targetID)
	if err != nil {
		return DeploymentTargetReview{}, err
	}
	var current DeploymentConfig
	err = s.DB.GetContext(ctx, &current, s.Q(`SELECT p.id project_id,p.name project_name,t.source_type,t.environment,t.server_id,s.name server_name,COALESCE(t.workspace_id,'') workspace_id,COALESCE(w.path,'') workspace_path,COALESCE(w.display_name,'') workspace_name,t.repository,t.git_ref,'' resolved_commit,t.compose_file,t.working_dir,t.build_mode,t.release_root,t.public_url configured_public_url,t.detected_public_url,t.health_checks,COALESCE(t.secret_set_id,'') secret_set_id,COALESCE(ss.name,'') secret_set_name,COALESCE(ss.key_version,0) secret_set_key_version,ss.updated_at secret_set_updated_at FROM deployment_targets t JOIN projects p ON p.id=t.project_id JOIN servers s ON s.id=t.server_id LEFT JOIN workspaces w ON w.id=t.workspace_id LEFT JOIN secret_sets ss ON ss.id=t.secret_set_id WHERE t.id=?`), targetID)
	if err != nil {
		return DeploymentTargetReview{}, err
	}
	last, lastErr := s.LatestDeploymentSnapshot(ctx, targetID)
	if errors.Is(lastErr, sql.ErrNoRows) {
		return DeploymentTargetReview{TargetID: targetID, Current: current, Changes: []DeploymentConfigChange{}, SnapshotAvailable: false}, nil
	}
	if lastErr != nil {
		return DeploymentTargetReview{}, lastErr
	}
	return DeploymentTargetReview{TargetID: targetID, Current: current, LastSuccessful: &last, Changes: deploymentConfigChanges(current, last.DeploymentConfig), SnapshotAvailable: true}, nil
}

func deploymentConfigChanges(current, previous DeploymentConfig) []DeploymentConfigChange {
	values := []struct {
		field, previous, current string
	}{
		{"source_type", previous.SourceType, current.SourceType},
		{"environment", previous.Environment, current.Environment},
		{"server", previous.ServerName, current.ServerName},
		{"workspace", previous.WorkspacePath, current.WorkspacePath},
		{"repository", previous.Repository, current.Repository},
		{"git_ref", previous.GitRef, current.GitRef},
		{"compose_file", previous.ComposeFile, current.ComposeFile},
		{"working_dir", previous.WorkingDir, current.WorkingDir},
		{"build_mode", previous.BuildMode, current.BuildMode},
		{"release_root", previous.ReleaseRoot, current.ReleaseRoot},
		{"configured_public_url", previous.ConfiguredPublicURL, current.ConfiguredPublicURL},
		{"detected_public_url", previous.DetectedPublicURL, current.DetectedPublicURL},
		{"health_checks", previous.HealthChecks, current.HealthChecks},
		{"secret_set", previous.SecretSetName, current.SecretSetName},
		{"secret_set_key_version", fmt.Sprintf("%d", previous.SecretSetKeyVersion), fmt.Sprintf("%d", current.SecretSetKeyVersion)},
	}
	changes := make([]DeploymentConfigChange, 0)
	for _, value := range values {
		if value.previous != value.current {
			changes = append(changes, DeploymentConfigChange{Field: value.field, Previous: value.previous, Current: value.current})
		}
	}
	return changes
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
	if update.Status == "succeeded" && strings.TrimSpace(update.DetectedPublicURL) != "" {
		if _, err := tx.ExecContext(ctx, s.Q("UPDATE deployment_targets SET detected_public_url=? WHERE id=?"), strings.TrimSpace(update.DetectedPublicURL), metadata.TargetID); err != nil {
			return err
		}
	}
	if update.Status == "succeeded" {
		detectedURL := strings.TrimSpace(update.DetectedPublicURL)
		if _, err := tx.ExecContext(ctx, s.Q(`UPDATE deployment_config_snapshots SET
			resolved_commit=CASE WHEN ?='' THEN resolved_commit ELSE ? END,
			detected_public_url=CASE WHEN ?='' THEN detected_public_url ELSE ? END
			WHERE deployment_id=?`), update.ResolvedCommit, update.ResolvedCommit, detectedURL, detectedURL, update.DeploymentID); err != nil {
			return err
		}
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
	case "start", "stop", "restart", "remove", "delete":
	default:
		return "", fmt.Errorf("unsupported container action %q", action)
	}
	if strings.TrimSpace(idempotency) == "" {
		return "", errors.New("container operation idempotency key is required")
	}
	var operationID string
	err := s.inSerializableTx(ctx, func(tx *sqlx.Tx) error {
		var existing string
		err := tx.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency)
		if err == nil {
			operationID = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var targetResource struct {
			ServerID    string `db:"server_id"`
			ProjectID   string `db:"project_id"`
			WorkspaceID string `db:"workspace_id"`
		}
		if err := tx.GetContext(ctx, &targetResource, s.Q("SELECT server_id,project_id,COALESCE(workspace_id,'') workspace_id FROM deployment_targets WHERE id=?"), targetID); err != nil {
			return err
		}
		if targetResource.ServerID != serverID {
			return errors.New("deployment target does not belong to the requested server")
		}
		var activeDeployments int
		if err := tx.GetContext(ctx, &activeDeployments, s.Q("SELECT COUNT(*) FROM deployments WHERE target_id=? AND status IN ('queued','preparing','running')"), targetID); err != nil {
			return err
		}
		if activeDeployments > 0 {
			return ErrDeploymentActive
		}
		var activeContainer int
		if err := tx.GetContext(ctx, &activeContainer, s.Q("SELECT COUNT(*) FROM deployment_container_state WHERE target_id=? AND status='pending'"), targetID); err != nil {
			return err
		}
		if activeContainer > 0 {
			return ErrDeploymentContainerActive
		}
		operationID = NewID()
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, s.Q("INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,idempotency_key,created_at) VALUES(?,?,?,NULLIF(?,''),?,?,?,?)"), operationID, serverID, targetResource.ProjectID, targetResource.WorkspaceID, protocol.OpDeployContainer, ciphertext, idempotency, now); err != nil {
			if lookupErr := tx.GetContext(ctx, &existing, s.Q("SELECT id FROM agent_operations WHERE idempotency_key=?"), idempotency); lookupErr == nil {
				operationID = existing
				return nil
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO deployment_container_state(target_id,operation_id,action,status,message,content,updated_at)
			VALUES(?,?,?,'pending',?,'',?)
			ON CONFLICT(target_id) DO UPDATE SET operation_id=excluded.operation_id,action=excluded.action,status='pending',message=excluded.message,content='',updated_at=excluded.updated_at`), targetID, operationID, action, "container "+action+" queued", now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
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
		case "remove", "delete":
			status = "removed"
		default:
			return fmt.Errorf("unsupported stored container action %q", state.Action)
		}
	}
	message = truncateText(strings.ToValidUTF8(message, "?"), 8192)
	content = truncateText(strings.ToValidUTF8(content, "?"), 1<<20)
	if state.Action == "delete" && status == "removed" {
		result, err := tx.ExecContext(ctx, s.Q("DELETE FROM deployment_targets WHERE id=?"), state.TargetID)
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
