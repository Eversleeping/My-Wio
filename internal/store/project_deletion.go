package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wio-platform/wio/internal/protocol"
)

const ProjectDeleteOperationKind = "git.project.delete"

var ErrProjectDeletionBlocked = errors.New("project deletion is blocked")

type ProjectDeletionWorkspace struct {
	ID             string `db:"id" json:"id"`
	ServerID       string `db:"server_id" json:"server_id"`
	ServerName     string `db:"server_name" json:"server_name"`
	ServerStatus   string `db:"server_status" json:"server_status"`
	Path           string `db:"path" json:"path"`
	ManagementMode string `db:"management_mode" json:"management_mode"`
	Kind           string `db:"kind" json:"kind"`
	Branch         string `db:"branch" json:"branch"`
	CommitSHA      string `db:"commit_sha" json:"commit_sha"`
	Dirty          int    `db:"dirty" json:"dirty"`
}

type ProjectDeletionBlocker struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Count   int      `json:"count"`
	Modes   []string `json:"modes"`
}

type ProjectDeletionPlan struct {
	ProjectID              string                     `db:"project_id" json:"project_id"`
	ProjectName            string                     `db:"project_name" json:"project_name"`
	RemoteURL              string                     `db:"remote_url" json:"remote_url"`
	RemotePreserved        bool                       `json:"remote_preserved"`
	CanDeleteMetadata      bool                       `json:"can_delete_metadata"`
	CanDeleteManagedFiles  bool                       `json:"can_delete_managed_files"`
	WorkspaceCount         int                        `json:"workspace_count"`
	ManagedWorkspaceCount  int                        `json:"managed_workspace_count"`
	ObservedWorkspaceCount int                        `json:"observed_workspace_count"`
	ActiveAgentOperations  int                        `json:"active_agent_operations"`
	ActiveCodexTasks       int                        `json:"active_codex_tasks"`
	ActiveDeployments      int                        `json:"active_deployments"`
	DirtyManagedWorkspaces int                        `json:"dirty_managed_workspaces"`
	OfflineManagedServers  int                        `json:"offline_managed_servers"`
	Workspaces             []ProjectDeletionWorkspace `json:"workspaces"`
	Blockers               []ProjectDeletionBlocker   `json:"blockers"`
}

type deletionQueryer interface {
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
}

func (s *Store) ProjectDeletionPlan(ctx context.Context, projectID string) (ProjectDeletionPlan, error) {
	return s.projectDeletionPlan(ctx, s.DB, projectID)
}

func (s *Store) projectDeletionPlan(ctx context.Context, query deletionQueryer, projectID string) (ProjectDeletionPlan, error) {
	var plan ProjectDeletionPlan
	if err := query.GetContext(ctx, &plan, s.Q(`SELECT id project_id,name project_name,remote_url FROM projects WHERE id=?`), projectID); err != nil {
		return ProjectDeletionPlan{}, err
	}
	plan.RemotePreserved = true
	plan.Workspaces = make([]ProjectDeletionWorkspace, 0)
	plan.Blockers = make([]ProjectDeletionBlocker, 0)
	if err := query.SelectContext(ctx, &plan.Workspaces, s.Q(`SELECT w.id,w.server_id,s.name server_name,CASE WHEN s.revoked_at IS NULL AND s.last_seen_at>? THEN 'online' ELSE 'offline' END server_status,w.path,w.management_mode,w.kind,w.branch,w.commit_sha,w.dirty FROM workspaces w JOIN servers s ON s.id=w.server_id WHERE w.project_id=? ORDER BY s.name,w.path`), time.Now().UTC().Add(-ServerOnlineGracePeriod), projectID); err != nil {
		return ProjectDeletionPlan{}, err
	}
	for _, workspace := range plan.Workspaces {
		plan.WorkspaceCount++
		if workspace.ManagementMode == "managed" {
			plan.ManagedWorkspaceCount++
			if workspace.Dirty != 0 {
				plan.DirtyManagedWorkspaces++
			}
			if workspace.ServerStatus != "online" {
				plan.OfflineManagedServers++
			}
		} else {
			plan.ObservedWorkspaceCount++
		}
	}
	countQueries := []struct {
		target *int
		query  string
	}{
		{&plan.ActiveAgentOperations, `SELECT COUNT(*) FROM agent_operations o WHERE (o.project_id=? OR o.workspace_id IN (SELECT id FROM workspaces WHERE project_id=?)) AND o.status IN ('queued','delivered','running')`},
		{&plan.ActiveCodexTasks, `SELECT COUNT(*) FROM codex_threads t JOIN workspaces w ON w.id=t.workspace_id WHERE w.project_id=? AND t.status IN ('queued','running')`},
		{&plan.ActiveDeployments, `SELECT
			(SELECT COUNT(*) FROM deployments d JOIN deployment_targets t ON t.id=d.target_id WHERE t.project_id=? AND d.status IN ('queued','preparing','running')) +
			(SELECT COUNT(*) FROM deployment_container_state cs JOIN deployment_targets t ON t.id=cs.target_id WHERE t.project_id=? AND cs.status='pending' AND cs.operation_id IS NOT NULL)`},
	}
	for _, item := range countQueries {
		args := []any{projectID}
		if item.target == &plan.ActiveAgentOperations || item.target == &plan.ActiveDeployments {
			args = append(args, projectID)
		}
		if err := query.GetContext(ctx, item.target, s.Q(item.query), args...); err != nil {
			return ProjectDeletionPlan{}, err
		}
	}
	commonModes := []string{"metadata-only", "managed-files"}
	plan.Blockers = appendDeletionBlocker(plan.Blockers, "active-agent-operations", "active Agent operations must finish before deleting the project", plan.ActiveAgentOperations, commonModes)
	plan.Blockers = appendDeletionBlocker(plan.Blockers, "active-codex-tasks", "active Codex tasks must finish before deleting the project", plan.ActiveCodexTasks, commonModes)
	plan.Blockers = appendDeletionBlocker(plan.Blockers, "active-deployments", "active deployments must finish before deleting the project", plan.ActiveDeployments, commonModes)
	plan.Blockers = appendDeletionBlocker(plan.Blockers, "dirty-managed-workspaces", "managed workspaces contain uncommitted changes", plan.DirtyManagedWorkspaces, []string{"managed-files"})
	plan.Blockers = appendDeletionBlocker(plan.Blockers, "offline-managed-servers", "managed workspace servers must be online", plan.OfflineManagedServers, []string{"managed-files"})
	plan.CanDeleteMetadata = plan.ActiveAgentOperations == 0 && plan.ActiveCodexTasks == 0 && plan.ActiveDeployments == 0
	plan.CanDeleteManagedFiles = plan.CanDeleteMetadata && plan.DirtyManagedWorkspaces == 0 && plan.OfflineManagedServers == 0
	return plan, nil
}

func appendDeletionBlocker(blockers []ProjectDeletionBlocker, code, message string, count int, modes []string) []ProjectDeletionBlocker {
	if count == 0 {
		return blockers
	}
	return append(blockers, ProjectDeletionBlocker{Code: code, Message: message, Count: count, Modes: modes})
}

func (s *Store) DeleteProjectMetadata(ctx context.Context, projectID string) (bool, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	plan, err := s.projectDeletionPlan(ctx, tx, projectID)
	if err != nil {
		return false, err
	}
	if !plan.CanDeleteMetadata {
		return false, ErrProjectDeletionBlocked
	}
	result, err := tx.ExecContext(ctx, s.Q(`DELETE FROM projects WHERE id=?`), projectID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *Store) QueueProjectManagedDeletion(ctx context.Context, projectID string) ([]string, bool, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	plan, err := s.projectDeletionPlan(ctx, tx, projectID)
	if err != nil {
		return nil, false, err
	}
	if !plan.CanDeleteManagedFiles {
		return nil, false, ErrProjectDeletionBlocked
	}
	managed := make([]ProjectDeletionWorkspace, 0, plan.ManagedWorkspaceCount)
	for _, workspace := range plan.Workspaces {
		if workspace.ManagementMode == "managed" {
			managed = append(managed, workspace)
		}
	}
	if len(managed) == 0 {
		if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM projects WHERE id=?`), projectID); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status='superseded' WHERE project_id=? AND kind=? AND status IN ('failed','canceled')`), projectID, ProjectDeleteOperationKind); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE projects SET status='deleting',provision_error='',updated_at=? WHERE id=?`), time.Now().UTC(), projectID); err != nil {
		return nil, false, err
	}
	hasWorktree := false
	for _, workspace := range managed {
		if workspace.Kind == "worktree" {
			hasWorktree = true
			break
		}
	}
	operationIDs := make([]string, 0, len(managed))
	for _, workspace := range managed {
		command := protocol.GitProjectDeleteCommand{ProjectID: projectID, WorkspaceID: workspace.ID, Path: workspace.Path}
		payload, err := json.Marshal(command)
		if err != nil {
			return nil, false, err
		}
		operationID := NewID()
		idempotency := "git-project-delete:" + projectID + ":" + workspace.ID + ":" + operationID
		status := "queued"
		if hasWorktree && workspace.Kind != "worktree" {
			status = "waiting"
		}
		if _, err := tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,status,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`), operationID, workspace.ServerID, projectID, workspace.ID, ProjectDeleteOperationKind, string(payload), status, 1, idempotency, time.Now().UTC()); err != nil {
			return nil, false, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return operationIDs, false, nil
}

func (s *Store) CompleteProjectDeletion(ctx context.Context, operation Operation, result protocol.OperationResult) error {
	if operation.Kind != ProjectDeleteOperationKind || operation.ProjectID == "" || operation.WorkspaceID == "" {
		return errors.New("invalid project deletion operation")
	}
	if result.OperationID != operation.ID {
		return errors.New("project deletion result operation mismatch")
	}
	var command protocol.GitProjectDeleteCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return fmt.Errorf("decode project deletion command: %w", err)
	}
	if command.ProjectID != operation.ProjectID || command.WorkspaceID != operation.WorkspaceID {
		return errors.New("project deletion operation resource mismatch")
	}
	if result.Status != "succeeded" {
		result.Status = "failed"
		if result.Message == "" {
			result.Message = "agent failed to delete managed workspace"
		}
	}
	if result.Status == "succeeded" {
		var deleted protocol.GitProjectDeleteResult
		if err := json.Unmarshal(result.Data, &deleted); err != nil || deleted.ProjectID != command.ProjectID || deleted.WorkspaceID != command.WorkspaceID || deleted.Path != command.Path {
			result.Status = "failed"
			result.Message = "invalid project deletion result"
		}
	}
	data, err := operationResultData(result.Data)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=? WHERE id=? AND kind=?`), result.Status, result.Message, data, time.Now().UTC(), operation.ID, ProjectDeleteOperationKind); err != nil {
		return err
	}
	if result.Status == "succeeded" {
		if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM workspaces WHERE id=? AND project_id=? AND management_mode='managed'`), operation.WorkspaceID, operation.ProjectID); err != nil {
			return err
		}
	}
	var active int
	if err := tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE project_id=? AND kind=? AND status IN ('queued','delivered','running')`), operation.ProjectID, ProjectDeleteOperationKind); err != nil {
		return err
	}
	if active > 0 {
		return tx.Commit()
	}
	var failed int
	if err := tx.GetContext(ctx, &failed, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE project_id=? AND kind=? AND status='failed'`), operation.ProjectID, ProjectDeleteOperationKind); err != nil {
		return err
	}
	if failed > 0 {
		message := result.Message
		if message == "" {
			message = "one or more managed workspaces could not be deleted"
		}
		if _, err := tx.ExecContext(ctx, s.Q(`UPDATE projects SET status='deletion_failed',provision_error=?,updated_at=? WHERE id=?`), message, time.Now().UTC(), operation.ProjectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status='canceled',result='blocked by an earlier project deletion failure',completed_at=? WHERE project_id=? AND kind=? AND status='waiting'`), time.Now().UTC(), operation.ProjectID, ProjectDeleteOperationKind); err != nil {
			return err
		}
		return tx.Commit()
	}
	var waiting int
	if err := tx.GetContext(ctx, &waiting, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE project_id=? AND kind=? AND status='waiting'`), operation.ProjectID, ProjectDeleteOperationKind); err != nil {
		return err
	}
	if waiting > 0 {
		if _, err := tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status='queued' WHERE project_id=? AND kind=? AND status='waiting'`), operation.ProjectID, ProjectDeleteOperationKind); err != nil {
			return err
		}
		return tx.Commit()
	}
	var remaining int
	if err := tx.GetContext(ctx, &remaining, s.Q(`SELECT COUNT(*) FROM workspaces WHERE project_id=? AND management_mode='managed'`), operation.ProjectID); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx, s.Q(`DELETE FROM projects WHERE id=?`), operation.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var _ deletionQueryer = (*sqlx.DB)(nil)
var _ deletionQueryer = (*sqlx.Tx)(nil)
