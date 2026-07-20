package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type WorkspaceDeletionPlan struct {
	WorkspaceID      string   `json:"workspace_id"`
	Path             string   `json:"path"`
	Managed          bool     `json:"managed"`
	Dirty            bool     `json:"dirty"`
	ActiveOperations int      `json:"active_operations"`
	ThreadCount      int      `json:"thread_count"`
	ChildWorkspaces  int      `json:"child_workspaces"`
	CanRemoveRecord  bool     `json:"can_remove_record"`
	CanDeleteFiles   bool     `json:"can_delete_files"`
	RecordBlockers   []string `json:"record_blockers"`
	FileBlockers     []string `json:"file_blockers"`
	Blockers         []string `json:"blockers"`
}

func (s *Store) UpdateWorkspaceDisplayName(ctx context.Context, workspaceID, displayName string) (Workspace, error) {
	result, err := s.DB.ExecContext(ctx, s.Q("UPDATE workspaces SET display_name=? WHERE id=?"), displayName, workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Workspace{}, err
	}
	if rows != 1 {
		return Workspace{}, sql.ErrNoRows
	}
	return s.Workspace(ctx, workspaceID)
}

func (s *Store) WorkspaceDeletionPlan(ctx context.Context, workspaceID string, force bool) (WorkspaceDeletionPlan, error) {
	workspace, err := s.Workspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceDeletionPlan{}, err
	}
	plan := WorkspaceDeletionPlan{WorkspaceID: workspace.ID, Path: workspace.Path, Managed: workspace.ManagementMode == "managed", Dirty: workspace.Dirty != 0, RecordBlockers: []string{}, FileBlockers: []string{}, Blockers: []string{}}
	if err = s.DB.GetContext(ctx, &plan.ActiveOperations, s.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND status IN ('queued','delivered','running')"), workspaceID); err != nil {
		return WorkspaceDeletionPlan{}, err
	}
	if err = s.DB.GetContext(ctx, &plan.ThreadCount, s.Q("SELECT COUNT(*) FROM codex_threads WHERE workspace_id=?"), workspaceID); err != nil {
		return WorkspaceDeletionPlan{}, err
	}
	if err = s.DB.GetContext(ctx, &plan.ChildWorkspaces, s.Q("SELECT COUNT(*) FROM workspaces WHERE parent_workspace_id=?"), workspaceID); err != nil {
		return WorkspaceDeletionPlan{}, err
	}
	if plan.ActiveOperations != 0 {
		plan.RecordBlockers = append(plan.RecordBlockers, "workspace has active operations")
	}
	if plan.ThreadCount != 0 {
		plan.RecordBlockers = append(plan.RecordBlockers, "workspace has Codex sessions")
	}
	if plan.ChildWorkspaces != 0 {
		plan.RecordBlockers = append(plan.RecordBlockers, "workspace has linked worktrees")
	}
	plan.CanRemoveRecord = len(plan.RecordBlockers) == 0
	plan.FileBlockers = append([]string(nil), plan.RecordBlockers...)
	if !plan.Managed {
		plan.FileBlockers = append(plan.FileBlockers, "workspace is not managed")
	}
	if plan.Dirty && !force {
		plan.FileBlockers = append(plan.FileBlockers, "workspace has uncommitted changes")
	}
	plan.CanDeleteFiles = len(plan.FileBlockers) == 0
	if !plan.CanDeleteFiles {
		plan.Blockers = append([]string(nil), plan.FileBlockers...)
	} else {
		plan.Blockers = append([]string(nil), plan.RecordBlockers...)
	}
	return plan, nil
}

func (s *Store) RemoveWorkspaceRecord(ctx context.Context, workspaceID string) error {
	plan, err := s.WorkspaceDeletionPlan(ctx, workspaceID, false)
	if err != nil {
		return err
	}
	if !plan.CanRemoveRecord {
		return errors.New("workspace metadata cannot be removed while dependencies exist")
	}
	result, err := s.DB.ExecContext(ctx, s.Q(`DELETE FROM workspaces WHERE id=?
		AND NOT EXISTS (SELECT 1 FROM agent_operations WHERE workspace_id=? AND status IN ('queued','delivered','running'))
		AND NOT EXISTS (SELECT 1 FROM codex_threads WHERE workspace_id=?)
		AND NOT EXISTS (SELECT 1 FROM workspaces child WHERE child.parent_workspace_id=?)`), workspaceID, workspaceID, workspaceID, workspaceID)
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
	return nil
}

func (s *Store) QueueWorkspaceLifecycle(ctx context.Context, workspace Workspace, command protocol.GitWorkspaceLifecycleCommand) (string, error) {
	if workspace.ManagementMode != "managed" {
		return "", errors.New("workspace is observed and cannot be modified")
	}
	if command.WorkspaceID != workspace.ID || command.ProjectID != workspace.ProjectID || command.SourcePath != workspace.Path {
		return "", errors.New("workspace lifecycle command does not match workspace")
	}
	if command.Action != "move" && command.Action != "copy" && command.Action != "delete" {
		return "", errors.New("unsupported workspace lifecycle action")
	}
	if (command.Action == "move" || command.Action == "copy") && strings.TrimSpace(command.TargetPath) == "" {
		return "", errors.New("target path is required")
	}
	if command.Action == "copy" && command.TargetWorkspaceID == "" {
		return "", errors.New("target workspace ID is required")
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var active int
	if err = tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND workspace_write=1 AND status IN ('queued','delivered','running')"), workspace.ID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrWorkspaceWriteActive
	}
	operationID := NewID()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,'git.workspace.lifecycle',?,1,?,?)`), operationID, workspace.ServerID, workspace.ProjectID, workspace.ID, string(payload), "workspace-lifecycle:"+workspace.ID+":"+operationID, now); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "operations_workspace_active_write_unique") || strings.Contains(lower, "unique constraint failed: agent_operations.workspace_id") {
			return "", ErrWorkspaceWriteActive
		}
		return "", err
	}
	lifecycleStatus := map[string]string{"move": "moving", "copy": "copying", "delete": "deleting"}[command.Action]
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=?,git_error='' WHERE id=?"), lifecycleStatus, workspace.ID); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
}

func (s *Store) QueueCrossServerWorkspaceClone(ctx context.Context, source Workspace, targetServerID string, command protocol.GitWorkspaceCloneCommand) (string, error) {
	if source.ManagementMode != "managed" || source.ServerID == targetServerID {
		return "", errors.New("cross-server clone requires a managed source and a different target server")
	}
	if command.ProjectID != source.ProjectID || command.WorkspaceID == "" || command.RemoteURL == "" || command.Branch == "" || command.ExpectedHead == "" || command.Destination == "" {
		return "", errors.New("cross-server clone command is incomplete")
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var targetCount int
	if err = tx.GetContext(ctx, &targetCount, s.Q("SELECT COUNT(*) FROM servers WHERE id=? AND revoked_at IS NULL"), targetServerID); err != nil {
		return "", err
	}
	if targetCount != 1 {
		return "", sql.ErrNoRows
	}
	var active int
	if err = tx.GetContext(ctx, &active, s.Q("SELECT COUNT(*) FROM agent_operations WHERE workspace_id=? AND workspace_write=1 AND status IN ('queued','delivered','running')"), source.ID); err != nil {
		return "", err
	}
	if active != 0 {
		return "", ErrWorkspaceWriteActive
	}
	operationID := NewID()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,workspace_id,kind,payload,workspace_write,idempotency_key,created_at) VALUES(?,?,?,?,'git.workspace.clone',?,1,?,?)`), operationID, targetServerID, source.ProjectID, source.ID, string(payload), "workspace-clone:"+source.ID+":"+command.WorkspaceID, now); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "operations_workspace_active_write_unique") || strings.Contains(lower, "unique constraint failed: agent_operations.workspace_id") {
			return "", ErrWorkspaceWriteActive
		}
		return "", err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status='copying',git_error='' WHERE id=?"), source.ID); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return operationID, nil
}

func (s *Store) CompleteCrossServerWorkspaceClone(ctx context.Context, operation Operation, command protocol.GitWorkspaceCloneCommand, result protocol.GitWorkspaceCloneResult, operationResult protocol.OperationResult) error {
	if operation.Kind != "git.workspace.clone" || operation.ProjectID != command.ProjectID || operation.WorkspaceID == "" || operation.ServerID == "" {
		return errors.New("workspace clone operation does not match command")
	}
	if result.WorkspaceID != command.WorkspaceID || result.ProjectID != command.ProjectID || cleanLifecyclePath(result.Path) != cleanLifecyclePath(command.Destination) || result.Branch != command.Branch || result.CommitSHA != command.ExpectedHead {
		return errors.New("workspace clone result does not match command")
	}
	data, err := operationResultData(operationResult.Data)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceCount int
	if err = tx.GetContext(ctx, &sourceCount, s.Q("SELECT COUNT(*) FROM workspaces WHERE id=? AND project_id=?"), operation.WorkspaceID, command.ProjectID); err != nil {
		return err
	}
	if sourceCount != 1 {
		return sql.ErrNoRows
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,parent_workspace_id,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at) VALUES(?,?,?,?,?,'managed','ready','primary',NULL,?,?,0,?,?)`), command.WorkspaceID, command.ProjectID, operation.ServerID, result.Path, command.Name, result.Branch, result.CommitSHA, now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status='ready',git_error='' WHERE id=?"), operation.WorkspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='succeeded',result=?,result_data=?,completed_at=? WHERE id=?"), operationResult.Message, data, now, operation.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailCrossServerWorkspaceClone(ctx context.Context, operation Operation, result protocol.OperationResult, workspaceStatus string) error {
	if workspaceStatus != "partial" {
		workspaceStatus = "ready"
	}
	data, err := operationResultData(result.Data)
	if err != nil {
		data = "{}"
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=?,git_error=? WHERE id=?"), workspaceStatus, result.Message, operation.WorkspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='failed',result=?,result_data=?,completed_at=? WHERE id=?"), result.Message, data, now, operation.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteWorkspaceLifecycle(ctx context.Context, operation Operation, command protocol.GitWorkspaceLifecycleCommand, result protocol.GitWorkspaceLifecycleResult, operationResult protocol.OperationResult) error {
	if operation.Kind != "git.workspace.lifecycle" || operation.ProjectID != command.ProjectID || operation.WorkspaceID != command.WorkspaceID || operation.ServerID == "" {
		return errors.New("workspace lifecycle operation does not match command")
	}
	if result.WorkspaceID != command.WorkspaceID || result.TargetWorkspaceID != command.TargetWorkspaceID || result.Action != command.Action || cleanLifecyclePath(result.SourcePath) != cleanLifecyclePath(command.SourcePath) {
		return errors.New("workspace lifecycle result does not match command")
	}
	if command.Action != "delete" && cleanLifecyclePath(result.TargetPath) != cleanLifecyclePath(command.TargetPath) {
		return errors.New("workspace lifecycle target does not match command")
	}
	data, err := operationResultData(operationResult.Data)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	switch command.Action {
	case "move":
		result, err := tx.ExecContext(ctx, s.Q("UPDATE workspaces SET path=?,status='ready',git_error='',last_scanned_at=? WHERE id=? AND server_id=?"), result.TargetPath, now, command.WorkspaceID, operation.ServerID)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return sql.ErrNoRows
		}
	case "copy":
		_, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,parent_workspace_id,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at)
			SELECT ?,project_id,server_id,?,?,'managed','ready','primary',NULL,branch,commit_sha,dirty,last_git_refresh_at,? FROM workspaces WHERE id=?`), command.TargetWorkspaceID, result.TargetPath, filepath.Base(result.TargetPath), now, command.WorkspaceID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status='ready',git_error='' WHERE id=?"), command.WorkspaceID); err != nil {
			return err
		}
	case "delete":
		result, err := tx.ExecContext(ctx, s.Q("DELETE FROM workspaces WHERE id=? AND server_id=?"), command.WorkspaceID, operation.ServerID)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return sql.ErrNoRows
		}
	default:
		return errors.New("unsupported workspace lifecycle action")
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='succeeded',result=?,result_data=?,completed_at=? WHERE id=?"), operationResult.Message, data, now, operationResult.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailWorkspaceLifecycle(ctx context.Context, operation Operation, command protocol.GitWorkspaceLifecycleCommand, result protocol.OperationResult, workspaceStatus string) error {
	if workspaceStatus != "partial" {
		workspaceStatus = "ready"
	}
	data, err := operationResultData(result.Data)
	if err != nil {
		data = "{}"
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE workspaces SET status=?,git_error=? WHERE id=?"), workspaceStatus, result.Message, command.WorkspaceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q("UPDATE agent_operations SET status='failed',result=?,result_data=?,completed_at=? WHERE id=?"), result.Message, data, now, operation.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func cleanLifecyclePath(value string) string {
	return filepath.Clean(strings.TrimSpace(value))
}

func ManagedRoots(server Server) []string {
	var roots []string
	_ = json.Unmarshal([]byte(server.ManagedRoots), &roots)
	return roots
}

func (s *Store) WorkspaceLifecycleCommand(ctx context.Context, operationID string) (Operation, protocol.GitWorkspaceLifecycleCommand, error) {
	operation, err := s.Operation(ctx, operationID)
	if err != nil {
		return Operation{}, protocol.GitWorkspaceLifecycleCommand{}, err
	}
	var command protocol.GitWorkspaceLifecycleCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return Operation{}, protocol.GitWorkspaceLifecycleCommand{}, fmt.Errorf("decode workspace lifecycle command: %w", err)
	}
	return operation, command, nil
}
