package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

const gitProjectCreateKind = "git.project.create"

// BlankProjectProvision is the control-plane state created before an agent
// starts initializing a repository. The workspace ID is reserved in the
// command payload, but the workspace row is deliberately created only after
// the agent confirms that the directory exists.
type BlankProjectProvision struct {
	Project     Project
	ServerID    string
	WorkspaceID string
	OperationID string
	Command     protocol.GitProjectCreateCommand
}

// CreateBlankProject atomically creates a provisioning project and its agent
// operation. No workspace row is inserted until the operation succeeds.
func (s *Store) CreateBlankProject(ctx context.Context, serverID, name, destination, initialBranch string, initializeREADME bool) (BlankProjectProvision, error) {
	return s.createBlankProject(ctx, serverID, name, destination, initialBranch, initializeREADME, BlankProjectRemoteSpec{}, "queued")
}

// CreateBlankProjectWithRemote reserves a project and queues the Agent command
// with an already-known remote URL (for example an existing empty repository).
func (s *Store) CreateBlankProjectWithRemote(ctx context.Context, serverID, name, destination, initialBranch string, initializeREADME bool, remote BlankProjectRemoteSpec) (BlankProjectProvision, error) {
	if remote.Mode == "" {
		remote.Mode = "existing"
	}
	return s.createBlankProject(ctx, serverID, name, destination, initialBranch, initializeREADME, remote, "queued")
}

// PrepareBlankProject reserves a project and an operation in preparing state.
// The operation is activated only after a provider-created remote is durable.
func (s *Store) PrepareBlankProject(ctx context.Context, serverID, name, destination, initialBranch string, initializeREADME bool, remote BlankProjectRemoteSpec) (BlankProjectProvision, error) {
	return s.createBlankProject(ctx, serverID, name, destination, initialBranch, initializeREADME, remote, "preparing")
}

func (s *Store) createBlankProject(ctx context.Context, serverID, name, destination, initialBranch string, initializeREADME bool, remote BlankProjectRemoteSpec, operationStatus string) (BlankProjectProvision, error) {
	projectID, workspaceID, operationID := NewID(), NewID(), NewID()
	command := protocol.GitProjectCreateCommand{
		ProjectID: projectID, WorkspaceID: workspaceID, Name: name,
		Destination: destination, InitialBranch: initialBranch,
		InitializeREADME: initializeREADME,
	}
	if remote.Mode == "existing" {
		command.RemoteURL = strings.TrimSpace(remote.URL)
	}
	command.RequireEmptyRemote = remote.Mode == "existing" || remote.Mode == "create"
	payload, err := json.Marshal(command)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	now := time.Now().UTC()
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO projects(id,name,remote_url,normalized_remote,default_branch,status,provision_error,created_at,updated_at) VALUES(?,?,?,?,?, 'provisioning', '', ?, ?)`), projectID, name, strings.TrimSpace(remote.URL), normalizeRemote(remote.URL), initialBranch, now, now); err != nil {
		return BlankProjectProvision{}, err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,kind,payload,idempotency_key,status,created_at) VALUES(?,?,?, ?,?,?,?,?)`), operationID, serverID, projectID, gitProjectCreateKind, string(payload), "git-project-create:"+projectID, operationStatus, now); err != nil {
		return BlankProjectProvision{}, err
	}
	if err = s.prepareProjectRemote(ctx, tx, projectID, remote, map[bool]string{true: "provisioning", false: "ready"}[operationStatus == "preparing"]); err != nil {
		return BlankProjectProvision{}, err
	}
	var project Project
	if err = tx.GetContext(ctx, &project, s.Q(`SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,0 workspace_count FROM projects p WHERE p.id=?`), projectID); err != nil {
		return BlankProjectProvision{}, err
	}
	if err = tx.Commit(); err != nil {
		return BlankProjectProvision{}, err
	}
	return BlankProjectProvision{Project: project, ServerID: serverID, WorkspaceID: workspaceID, OperationID: operationID, Command: command}, nil
}

// ActivateBlankProjectRemote records the provider result and releases the
// queued Agent operation atomically. No token or provider response is stored
// in the operation payload.
func (s *Store) ActivateBlankProjectRemote(ctx context.Context, projectID, operationID string, remote protocol.ProjectRemoteResult) (BlankProjectProvision, error) {
	if strings.TrimSpace(remote.FetchURL) == "" || strings.TrimSpace(remote.PushURL) == "" {
		return BlankProjectProvision{}, errors.New("project remote URLs are required")
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	defer tx.Rollback()
	var operation Operation
	if err = tx.GetContext(ctx, &operation, s.Q(operationSelect+` WHERE id=?`), operationID); err != nil {
		return BlankProjectProvision{}, err
	}
	if operation.ProjectID != projectID || operation.Kind != gitProjectCreateKind || operation.Status != "preparing" {
		return BlankProjectProvision{}, errRemoteOperationState
	}
	command, err := operationCommand(operation)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	command.RemoteURL = strings.TrimSpace(remote.FetchURL)
	payload, err := json.Marshal(command)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE projects SET remote_url=?,normalized_remote=?,updated_at=? WHERE id=?`), command.RemoteURL, normalizeRemote(command.RemoteURL), now, projectID); err != nil {
		return BlankProjectProvision{}, err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE project_remotes SET provider=?,namespace=?,repository=?,fetch_url=?,push_url=?,web_url=?,status='ready',error='',updated_at=? WHERE project_id=? AND name='origin'`), remote.Provider, remote.Namespace, remote.Repository, remote.FetchURL, remote.PushURL, remote.WebURL, now, projectID); err != nil {
		return BlankProjectProvision{}, err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET payload=?,status='queued' WHERE id=?`), string(payload), operationID); err != nil {
		return BlankProjectProvision{}, err
	}
	if err = tx.Commit(); err != nil {
		return BlankProjectProvision{}, err
	}
	project, err := s.Project(ctx, projectID)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	return BlankProjectProvision{Project: project, ServerID: operation.ServerID, WorkspaceID: command.WorkspaceID, OperationID: operationID, Command: command}, nil
}

// RetryBlankProject requeues the exact command from the latest failed/partial
// operation. Keeping the same idempotency key would return the old operation,
// so a retry receives a unique key while retaining all command IDs and paths.
func (s *Store) RetryBlankProject(ctx context.Context, projectID string) (BlankProjectProvision, error) {
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	defer tx.Rollback()
	var project Project
	if err = tx.GetContext(ctx, &project, s.Q(`SELECT p.id,p.name,p.description,p.remote_url,p.default_branch,p.status,p.provision_error,p.pinned_at,p.hidden_at,p.archived_at,p.updated_at,(SELECT COUNT(*) FROM workspaces w WHERE w.project_id=p.id) workspace_count FROM projects p WHERE p.id=?`), projectID); err != nil {
		return BlankProjectProvision{}, err
	}
	if project.Status != "failed" && project.Status != "partial" {
		return BlankProjectProvision{}, errors.New("only a failed or partial blank project can be retried")
	}
	var row struct {
		OperationID string `db:"id"`
		ServerID    string `db:"server_id"`
		Payload     string `db:"payload"`
	}
	err = tx.GetContext(ctx, &row, s.Q(`SELECT id,server_id,payload FROM agent_operations WHERE project_id=? AND kind=? ORDER BY created_at DESC LIMIT 1`), projectID, gitProjectCreateKind)
	if err != nil {
		return BlankProjectProvision{}, err
	}
	var command protocol.GitProjectCreateCommand
	if err = json.Unmarshal([]byte(row.Payload), &command); err != nil {
		return BlankProjectProvision{}, fmt.Errorf("invalid blank project command: %w", err)
	}
	if command.ProjectID != projectID || command.WorkspaceID == "" || command.Name == "" {
		return BlankProjectProvision{}, errors.New("blank project command does not match project")
	}
	var active int
	if err = tx.GetContext(ctx, &active, s.Q(`SELECT COUNT(*) FROM agent_operations WHERE project_id=? AND kind=? AND status IN ('queued','delivered','running')`), projectID, gitProjectCreateKind); err != nil {
		return BlankProjectProvision{}, err
	}
	if active != 0 {
		return BlankProjectProvision{}, errors.New("blank project creation is already active")
	}
	now := time.Now().UTC()
	operationID := NewID()
	payload, _ := json.Marshal(command)
	operationStatus := "queued"
	var remoteMode, remoteStatus, remoteURL string
	remoteErr := tx.QueryRowxContext(ctx, s.Q(`SELECT mode,status,fetch_url FROM project_remotes WHERE project_id=? AND name='origin'`), projectID).Scan(&remoteMode, &remoteStatus, &remoteURL)
	if remoteErr == nil && remoteMode == "create" && remoteStatus != "ready" {
		operationStatus = "preparing"
		command.RemoteURL = ""
		payload, _ = json.Marshal(command)
		if _, err = tx.ExecContext(ctx, s.Q(`UPDATE project_remotes SET status='provisioning',error='',updated_at=? WHERE project_id=? AND name='origin'`), now, projectID); err != nil {
			return BlankProjectProvision{}, err
		}
	} else if remoteErr == nil && remoteMode == "create" && remoteStatus == "ready" {
		command.RemoteURL = remoteURL
		payload, _ = json.Marshal(command)
	} else if remoteErr != nil && !errors.Is(remoteErr, sql.ErrNoRows) {
		return BlankProjectProvision{}, remoteErr
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE projects SET status='provisioning',provision_error='',updated_at=? WHERE id=?`), now, projectID); err != nil {
		return BlankProjectProvision{}, err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO agent_operations(id,server_id,project_id,kind,payload,idempotency_key,status,created_at) VALUES(?,?,?, ?,?,?,?,?)`), operationID, row.ServerID, projectID, gitProjectCreateKind, string(payload), "git-project-retry:"+projectID+":"+operationID, operationStatus, now); err != nil {
		return BlankProjectProvision{}, err
	}
	if err = tx.Commit(); err != nil {
		return BlankProjectProvision{}, err
	}
	project.Status = "provisioning"
	project.ProvisionError = ""
	return BlankProjectProvision{Project: project, ServerID: row.ServerID, WorkspaceID: command.WorkspaceID, OperationID: operationID, Command: command}, nil
}

// CommitBlankProject persists a successful agent result and marks the
// operation complete in one transaction. It never removes the agent-created
// directory if the database transaction fails.
func (s *Store) CommitBlankProject(ctx context.Context, operation Operation, command protocol.GitProjectCreateCommand, result protocol.GitProjectCreateResult, operationResult protocol.OperationResult) error {
	if operation.Kind != gitProjectCreateKind || operation.ProjectID != command.ProjectID || operation.WorkspaceID != "" || operation.ServerID == "" {
		return errors.New("blank project operation does not match command")
	}
	if err := ValidateBlankProjectResult(command, result); err != nil {
		return err
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.GetContext(ctx, &count, s.Q("SELECT COUNT(*) FROM projects WHERE id=? AND status='provisioning'"), command.ProjectID); err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, s.Q(`INSERT INTO workspaces(id,project_id,server_id,path,display_name,management_mode,status,kind,branch,commit_sha,dirty,last_git_refresh_at,last_scanned_at) VALUES(?,?,?,?,?,'managed','ready','primary',?,?,0,?,?)`), command.WorkspaceID, command.ProjectID, operation.ServerID, result.Path, command.Name, result.Branch, result.CommitSHA, time.Now().UTC(), time.Now().UTC()); err != nil {
		return err
	}
	data, err := operationResultData(operationResult.Data)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE projects SET status='ready',provision_error='',updated_at=? WHERE id=?`), now, command.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status=?,result=?,result_data=?,completed_at=? WHERE id=? AND project_id=?`), operationResult.Status, operationResult.Message, data, now, operationResult.OperationID, command.ProjectID); err != nil {
		return err
	}
	return tx.Commit()
}

// ValidateBlankProjectResult checks the fields whose values are determined by
// the requested command. It is shared by the gateway before the persistence
// transaction so protocol mismatches become explicit operation failures.
func ValidateBlankProjectResult(command protocol.GitProjectCreateCommand, result protocol.GitProjectCreateResult) error {
	if command.ProjectID == "" || command.WorkspaceID == "" || result.Path == "" || !path.IsAbs(normalizeRemotePath(result.Path)) || result.Branch != command.InitialBranch {
		return errors.New("blank project result does not match command")
	}
	if strings.HasPrefix(normalizeRemotePath(command.Destination), "/") && normalizeRemotePath(result.Path) != normalizeRemotePath(command.Destination) {
		return errors.New("blank project result path does not match command")
	}
	if strings.TrimSpace(result.RemoteURL) != strings.TrimSpace(command.RemoteURL) {
		return errors.New("blank project result remote does not match command")
	}
	if command.InitializeREADME && (result.Unborn || result.CommitSHA == "") {
		return errors.New("blank project README result is missing an initial commit")
	}
	if !command.InitializeREADME && (!result.Unborn || result.CommitSHA != "") {
		return errors.New("blank project result unexpectedly contains a commit")
	}
	return nil
}

// FailBlankProject records an agent or persistence failure. partial is used
// when the agent already created files but the success transaction failed.
func (s *Store) FailBlankProject(ctx context.Context, operation Operation, command protocol.GitProjectCreateCommand, operationResult protocol.OperationResult, status string) error {
	if status != "failed" && status != "partial" {
		status = "failed"
	}
	data, err := operationResultData(operationResult.Data)
	if err != nil {
		data = "{}"
	}
	tx, err := s.DB.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE projects SET status=?,provision_error=?,updated_at=? WHERE id=?`), status, operationResult.Message, now, command.ProjectID); err != nil {
		return err
	}
	remoteStatus := "failed"
	if status == "partial" {
		remoteStatus = "ready"
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE project_remotes SET status=CASE WHEN fetch_url<>'' THEN ? ELSE 'failed' END,error=?,updated_at=? WHERE project_id=? AND name='origin'`), remoteStatus, operationResult.Message, now, command.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.Q(`UPDATE agent_operations SET status='failed',result=?,result_data=?,completed_at=? WHERE id=? AND project_id=?`), operationResult.Message, data, now, operationResult.OperationID, command.ProjectID); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeRemotePath(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return ""
	}
	return path.Clean(raw)
}
