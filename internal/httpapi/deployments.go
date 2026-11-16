package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/deployer"
	"github.com/wio-platform/wio/internal/deploymenttemplate"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *API) deploymentTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := a.store.ListDeploymentTargets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list deployment targets")
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

func (a *API) deploymentTargetReview(w http.ResponseWriter, r *http.Request) {
	review, err := a.store.DeploymentTargetReview(r.Context(), chi.URLParam(r, "targetID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment configuration review")
		return
	}
	writeJSON(w, http.StatusOK, review)
}

type deploymentTargetInput struct {
	ProjectID    string                 `json:"project_id"`
	ServerID     string                 `json:"server_id"`
	SourceType   string                 `json:"source_type"`
	WorkspaceID  string                 `json:"workspace_id"`
	SecretSetID  string                 `json:"secret_set_id"`
	Environment  string                 `json:"environment"`
	Repository   string                 `json:"repository"`
	GitRef       string                 `json:"git_ref"`
	ComposeFile  string                 `json:"compose_file"`
	WorkingDir   string                 `json:"working_dir"`
	BuildMode    string                 `json:"build_mode"`
	HealthChecks []protocol.HealthCheck `json:"health_checks"`
	ReleaseRoot  string                 `json:"release_root"`
	PublicURL    string                 `json:"public_url"`
}

func (a *API) deploymentTargetFromInput(ctx context.Context, input deploymentTargetInput) (store.DeploymentTarget, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ServerID = strings.TrimSpace(input.ServerID)
	input.SourceType = strings.TrimSpace(input.SourceType)
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.SecretSetID = strings.TrimSpace(input.SecretSetID)
	input.Environment = strings.TrimSpace(input.Environment)
	input.Repository = strings.TrimSpace(input.Repository)
	input.GitRef = strings.TrimSpace(input.GitRef)
	input.ComposeFile = strings.TrimSpace(input.ComposeFile)
	input.WorkingDir = ""
	input.BuildMode = strings.TrimSpace(input.BuildMode)
	input.PublicURL = strings.TrimSpace(input.PublicURL)
	input.ReleaseRoot = "/var/lib/wio-agent/releases"
	if input.SourceType == "" {
		input.SourceType = "remote"
	}
	if input.ServerID == "" || input.Environment == "" {
		return store.DeploymentTarget{}, errors.New("server_id and environment are required")
	}
	if input.BuildMode != "" && input.BuildMode != "build" && input.BuildMode != "pull" {
		return store.DeploymentTarget{}, errors.New("build_mode must be build or pull")
	}
	if input.ComposeFile == "" {
		input.ComposeFile = "compose.yaml"
	}
	if input.BuildMode == "" {
		input.BuildMode = "build"
	}
	if input.PublicURL != "" {
		publicURL, err := normalizePublicURL(input.PublicURL)
		if err != nil {
			return store.DeploymentTarget{}, errors.New("public_url must use http or https and include a host")
		}
		input.PublicURL = publicURL
	}
	for _, check := range input.HealthChecks {
		if (check.Type != "http" && check.Type != "https" && check.Type != "tcp") || strings.TrimSpace(check.Address) == "" {
			return store.DeploymentTarget{}, errors.New("health checks must use http, https, or tcp and include an address")
		}
	}
	server, err := a.store.Server(ctx, input.ServerID)
	if err != nil {
		return store.DeploymentTarget{}, inputStoreFailure{err}
	}
	if server.Status != "online" {
		return store.DeploymentTarget{}, errors.New("target server must be online")
	}
	switch input.SourceType {
	case "workspace":
		workspace, err := a.store.Workspace(ctx, input.WorkspaceID)
		if err != nil {
			return store.DeploymentTarget{}, inputStoreFailure{err}
		}
		if workspace.ServerID != input.ServerID || workspace.Status != "ready" {
			return store.DeploymentTarget{}, errors.New("select an available project workspace on the target server")
		}
		input.ProjectID = workspace.ProjectID
		project, err := a.store.Project(ctx, workspace.ProjectID)
		if err != nil {
			return store.DeploymentTarget{}, inputStoreFailure{err}
		}
		input.Repository = project.RemoteURL
		if input.GitRef == "" {
			input.GitRef = workspace.Branch
			if input.GitRef == "" {
				input.GitRef = "HEAD"
			}
		}
	case "remote":
		input.WorkspaceID = ""
		if err := gitRemoteCredentialError(input.Repository); err != nil {
			return store.DeploymentTarget{}, err
		}
		if input.Repository == "" || !validGitRemote(input.Repository) {
			return store.DeploymentTarget{}, errors.New("repository must use HTTPS or SSH")
		}
		if input.GitRef == "" {
			input.GitRef = "main"
		}
		project, err := a.store.ProjectByRemote(ctx, input.Repository)
		if errors.Is(err, sql.ErrNoRows) {
			project, err = a.store.CreateProject(ctx, repositoryName(input.Repository), input.Repository)
		}
		if err != nil {
			return store.DeploymentTarget{}, inputStoreFailure{err}
		}
		input.ProjectID = project.ID
	default:
		return store.DeploymentTarget{}, errors.New("source_type must be workspace or remote")
	}
	// The Agent validates the ref again before fetching; rejecting it here
	// keeps refs Git would parse as options out of stored targets.
	if err := deployer.ValidateGitRef(input.GitRef); err != nil {
		return store.DeploymentTarget{}, fmt.Errorf("git_ref: %w", err)
	}
	checks, err := json.Marshal(input.HealthChecks)
	if err != nil {
		return store.DeploymentTarget{}, err
	}
	return store.DeploymentTarget{ProjectID: input.ProjectID, ServerID: input.ServerID, SourceType: input.SourceType, WorkspaceID: input.WorkspaceID, SecretSetID: input.SecretSetID, Environment: input.Environment, Repository: input.Repository, GitRef: input.GitRef, ComposeFile: input.ComposeFile, WorkingDir: input.WorkingDir, BuildMode: input.BuildMode, HealthChecks: string(checks), ReleaseRoot: input.ReleaseRoot, PublicURL: input.PublicURL}, nil
}

func normalizePublicURL(value string) (string, error) {
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("invalid public URL")
	}
	return parsed.String(), nil
}

func (a *API) createDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	var input deploymentTargetInput
	if !decodeJSON(w, r, &input) {
		return
	}
	targetInput, err := a.deploymentTargetFromInput(r.Context(), input)
	if err != nil {
		writeInputError(w, err)
		return
	}
	target, err := a.store.CreateDeploymentTarget(r.Context(), targetInput)
	if err != nil {
		if databaseConflict(err) {
			writeError(w, http.StatusConflict, "deployment target already exists")
		} else {
			writeStoreError(w, err, "could not create deployment target")
		}
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "deployment.target.create", "deployment_target", target.ID, map[string]string{"project_id": target.ProjectID, "environment": target.Environment}, clientIP(r))
	writeJSON(w, http.StatusCreated, target)
}

func (a *API) updateDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	var input deploymentTargetInput
	if !decodeJSON(w, r, &input) {
		return
	}
	target, err := a.deploymentTargetFromInput(r.Context(), input)
	if err != nil {
		writeInputError(w, err)
		return
	}
	target.ID = chi.URLParam(r, "targetID")
	target, err = a.store.UpdateDeploymentTarget(r.Context(), target)
	if errors.Is(err, store.ErrDeploymentActive) || errors.Is(err, store.ErrDeploymentContainerActive) {
		writeError(w, http.StatusConflict, "active deployments and container operations must finish before editing the target")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	if err != nil {
		if databaseConflict(err) {
			writeError(w, http.StatusConflict, "deployment target already exists")
		} else {
			writeStoreError(w, err, "could not update deployment target")
		}
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "deployment.target.update", "deployment_target", target.ID, map[string]string{"project_id": target.ProjectID, "environment": target.Environment}, clientIP(r))
	writeJSON(w, http.StatusOK, target)
}

func (a *API) deleteDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "targetID")
	target, err := a.store.DeploymentTarget(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment target")
		return
	}
	server, err := a.store.Server(r.Context(), target.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "target server must be online before deleting the target")
		return
	}
	environment, err := a.deploymentEnvironment(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not decrypt deployment secrets")
		return
	}
	command := protocol.ContainerActionCommand{TargetID: target.ID, Action: "delete", PublicURL: target.ConfiguredPublicURL, ReleaseRoot: target.ReleaseRoot, ComposeFile: target.ComposeFile, WorkingDir: target.WorkingDir, Environment: environment}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect target deletion operation")
		return
	}
	operationID, err := a.store.QueueDeploymentContainerOperation(r.Context(), target.ID, target.ServerID, "delete", ciphertext, "target-delete:"+target.ID+":"+store.NewID())
	if errors.Is(err, store.ErrDeploymentActive) || errors.Is(err, store.ErrDeploymentContainerActive) {
		writeError(w, http.StatusConflict, "active deployments and container operations must finish before deleting the target")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue deployment target deletion")
		return
	}
	a.gateway.Wake(target.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "deployment.target.delete", "deployment_target", id, map[string]string{"operation_id": operationID, "project_id": target.ProjectID, "environment": target.Environment}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "target_id": target.ID, "action": "delete"})
}

func (a *API) deployments(w http.ResponseWriter, r *http.Request) {
	deployments, err := a.store.ListDeployments(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list deployments")
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (a *API) operations(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, err := parseThreadPageValue(r.URL.Query()["offset"], r.URL.Query().Has("offset"), "offset", 0, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	operations, err := a.store.ListOperationsPage(r.Context(), r.URL.Query().Get("status"), limit, offset)
	if errors.Is(err, store.ErrInvalidOperationStatus) {
		writeError(w, http.StatusBadRequest, "invalid operation status")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list operations")
		return
	}
	if operations == nil {
		operations = []store.OperationSummary{}
	}
	writeJSON(w, http.StatusOK, operations)
}

func (a *API) deploymentDetails(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "deploymentID")
	deployment, err := a.store.Deployment(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment")
		return
	}
	events, err := a.store.DeploymentEvents(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment logs")
		return
	}
	if events == nil {
		events = []store.DeploymentEvent{}
	}
	if snapshot, snapshotErr := a.store.DeploymentSnapshot(r.Context(), id); snapshotErr == nil {
		deployment.Snapshot = &snapshot
	} else if errors.Is(snapshotErr, sql.ErrNoRows) {
		deployment.SnapshotError = "deployment configuration snapshot is unavailable"
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployment": deployment, "events": events})
}

func (a *API) deleteDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "deploymentID")
	if err := a.store.DeleteDeployment(r.Context(), id); errors.Is(err, store.ErrDeploymentActive) {
		writeError(w, http.StatusConflict, "active deployments cannot be deleted")
		return
	} else if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete deployment")
		return
	}
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "deployment.delete", "deployment", id, nil, clientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) startDeployment(w http.ResponseWriter, r *http.Request) {
	target, err := a.store.DeploymentTarget(r.Context(), chi.URLParam(r, "targetID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment target")
		return
	}
	server, err := a.store.Server(r.Context(), target.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "target server must be online before deployment")
		return
	}
	if target.SourceType == "workspace" && (target.WorkspaceID == "" || target.WorkspacePath == "") {
		writeError(w, http.StatusConflict, "selected project workspace is no longer available")
		return
	}
	var input struct {
		CommitRef string `json:"commit_ref"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.CommitRef = strings.TrimSpace(input.CommitRef)
	if input.CommitRef == "" {
		input.CommitRef = target.GitRef
	}
	// Mirror the Agent: a workspace deployment without a ref deploys HEAD.
	if input.CommitRef != "" || target.SourceType != "workspace" {
		if err := deployer.ValidateGitRef(input.CommitRef); err != nil {
			writeError(w, http.StatusBadRequest, "commit_ref: "+err.Error())
			return
		}
	}
	deployment, err := a.store.CreateDeployment(r.Context(), target.ID, input.CommitRef)
	if errors.Is(err, store.ErrDeploymentActive) || errors.Is(err, store.ErrDeploymentContainerActive) {
		writeError(w, http.StatusConflict, "another deployment or container operation is already active")
		return
	}
	if err != nil {
		writeStoreError(w, err, "could not create deployment")
		return
	}
	_ = a.store.AddDeploymentEvent(r.Context(), store.DeploymentEvent{DeploymentID: deployment.ID, Status: "queued", Message: "deployment queued", Content: "Waiting for the target Agent to start deployment."})
	snapshot, err := a.store.DeploymentSnapshot(r.Context(), deployment.ID)
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not load deployment snapshot"})
		writeError(w, http.StatusInternalServerError, "could not load deployment snapshot")
		return
	}
	server, err = a.store.Server(r.Context(), snapshot.ServerID)
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not load deployment server"})
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "target server is no longer online"})
		writeError(w, http.StatusConflict, "target server must be online before deployment")
		return
	}
	if snapshot.SourceType == "workspace" && (snapshot.WorkspaceID == "" || snapshot.WorkspacePath == "") {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "selected project workspace is no longer available"})
		writeError(w, http.StatusConflict, "selected project workspace is no longer available")
		return
	}
	environment, err := a.deploymentEnvironmentForSnapshot(r.Context(), snapshot)
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not decrypt deployment secrets"})
		writeError(w, http.StatusInternalServerError, "could not decrypt deployment secrets")
		return
	}
	var checks []protocol.HealthCheck
	_ = json.Unmarshal([]byte(snapshot.HealthChecks), &checks)
	serverAddress := strings.TrimSpace(server.Address)
	if serverAddress == "" {
		serverAddress = strings.TrimSpace(server.Hostname)
	}
	command := protocol.DeployCommand{DeploymentID: deployment.ID, TargetID: target.ID, DeploymentMode: deploymenttemplate.ModeAuto, ServerAddress: serverAddress, PublicURL: snapshot.ConfiguredPublicURL, SourceType: snapshot.SourceType, SourcePath: snapshot.WorkspacePath, Repository: snapshot.Repository, CommitRef: snapshot.GitRef, ComposeFile: snapshot.ComposeFile, WorkingDir: snapshot.WorkingDir, BuildMode: snapshot.BuildMode, ReleaseRoot: snapshot.ReleaseRoot, Environment: environment, HealthChecks: checks}
	command.Recovery, err = a.deploymentRecoveryForSnapshot(r.Context(), snapshot)
	if errors.Is(err, store.ErrDeploymentSnapshotUnavailable) || errors.Is(err, errDeploymentSecretsUndecryptable) {
		command.Recovery = nil
		_ = a.store.AddDeploymentEvent(r.Context(), store.DeploymentEvent{DeploymentID: deployment.ID, Status: "queued", Message: "historical recovery configuration unavailable", Content: "Historical deployment secrets are unavailable. Automatic recovery will use this deployment's configuration as a best effort."})
	} else if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not load historical recovery configuration"})
		writeError(w, http.StatusInternalServerError, "could not load historical recovery configuration")
		return
	}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not protect deployment operation"})
		writeError(w, http.StatusInternalServerError, "could not protect deployment operation")
		return
	}
	operationID, err := a.store.QueueEncryptedResourceOperation(r.Context(), snapshot.ServerID, protocol.OpDeployStart, ciphertext, "deploy:"+deployment.ID, store.OperationResource{ProjectID: snapshot.ProjectID, WorkspaceID: snapshot.WorkspaceID})
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not queue deployment"})
		writeError(w, http.StatusInternalServerError, "could not queue deployment")
		return
	}
	_ = a.store.AttachDeploymentOperation(r.Context(), deployment.ID, operationID)
	a.gateway.Wake(snapshot.ServerID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "deployment.start", "deployment", deployment.ID, map[string]string{"target_id": target.ID, "commit_ref": input.CommitRef}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment": deployment, "operation_id": operationID})
}

func (a *API) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	target, err := a.store.DeploymentTarget(r.Context(), chi.URLParam(r, "targetID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment target")
		return
	}
	server, err := a.store.Server(r.Context(), target.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "target server must be online before rollback")
		return
	}
	if !server.Supports(protocol.CapabilityDeployRollbackExact) {
		writeError(w, http.StatusConflict, "target Agent must be upgraded before selecting a historical rollback release")
		return
	}
	var input struct {
		DeploymentID string `json:"deployment_id"`
	}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &input) {
			return
		}
	}
	sourceID := strings.TrimSpace(input.DeploymentID)
	if sourceID == "" {
		latest, latestErr := a.store.LatestDeploymentSnapshot(r.Context(), target.ID)
		if errors.Is(latestErr, sql.ErrNoRows) {
			writeError(w, http.StatusConflict, "no successful deployment with a configuration snapshot is available for rollback")
			return
		}
		if latestErr != nil {
			writeError(w, http.StatusInternalServerError, "could not load rollback baseline")
			return
		}
		sourceID = latest.DeploymentID
	}
	source, err := a.store.DeploymentSnapshot(r.Context(), sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusConflict, "the selected deployment has no compatible configuration snapshot")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load rollback baseline")
		return
	}
	if source.TargetID != target.ID || source.ServerID != target.ServerID {
		writeError(w, http.StatusConflict, "the selected historical release does not belong to the current deployment target")
		return
	}
	deployment, err := a.store.CreateRollbackDeployment(r.Context(), target.ID, source.DeploymentID)
	if errors.Is(err, store.ErrDeploymentActive) || errors.Is(err, store.ErrDeploymentContainerActive) {
		writeError(w, http.StatusConflict, "another deployment or container operation is already active")
		return
	}
	if errors.Is(err, store.ErrDeploymentSnapshotUnavailable) {
		writeError(w, http.StatusConflict, "the selected deployment has no compatible configuration snapshot")
		return
	}
	if err != nil {
		writeStoreError(w, err, "could not create rollback")
		return
	}
	_ = a.store.AddDeploymentEvent(r.Context(), store.DeploymentEvent{DeploymentID: deployment.ID, Status: "queued", Message: "rollback queued", Content: "Waiting for the target Agent to restore the selected historical release."})
	environment, err := a.deploymentEnvironmentForSnapshot(r.Context(), source)
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not decrypt rollback secrets"})
		if errors.Is(err, store.ErrDeploymentSnapshotUnavailable) {
			writeError(w, http.StatusConflict, "the selected deployment secret version is unavailable")
		} else {
			writeError(w, http.StatusInternalServerError, "could not decrypt rollback secrets")
		}
		return
	}
	publicURL := source.ConfiguredPublicURL
	if publicURL == "" {
		publicURL = source.DetectedPublicURL
	}
	command := protocol.RollbackCommand{DeploymentID: deployment.ID, TargetID: target.ID, ReleaseDeploymentID: source.DeploymentID, ReleaseRoot: source.ReleaseRoot, ComposeFile: source.ComposeFile, WorkingDir: source.WorkingDir, PublicURL: publicURL, Environment: environment}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not protect rollback operation"})
		writeError(w, http.StatusInternalServerError, "could not protect rollback operation")
		return
	}
	operationID, err := a.store.QueueEncryptedResourceOperation(r.Context(), target.ServerID, protocol.OpDeployRollback, ciphertext, "rollback:"+deployment.ID, store.OperationResource{ProjectID: target.ProjectID, WorkspaceID: target.WorkspaceID})
	if err != nil {
		_ = a.store.SaveDeploymentStatus(r.Context(), protocol.DeploymentStatus{DeploymentID: deployment.ID, Status: "failed", Message: "could not queue rollback"})
		writeError(w, http.StatusInternalServerError, "could not queue rollback")
		return
	}
	_ = a.store.AttachDeploymentOperation(r.Context(), deployment.ID, operationID)
	a.gateway.Wake(target.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "deployment.rollback", "deployment", deployment.ID, map[string]string{"target_id": target.ID, "operation_id": operationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"deployment": deployment, "operation_id": operationID})
}

func (a *API) deploymentEnvironment(ctx context.Context, target store.DeploymentTarget) (map[string]string, error) {
	return a.deploymentEnvironmentForSecretSet(ctx, target.SecretSetID)
}

func (a *API) deploymentEnvironmentForSecretSet(ctx context.Context, secretSetID string) (map[string]string, error) {
	environment := map[string]string{}
	if secretSetID != "" {
		ciphertext, err := a.store.SecretCiphertext(ctx, secretSetID)
		if err != nil {
			return nil, err
		}
		if err := a.vault.Decrypt(ciphertext, &environment); err != nil {
			return nil, err
		}
	}
	return environment, nil
}

func (a *API) deploymentRecoveryForSnapshot(ctx context.Context, snapshot store.DeploymentSnapshot) (*protocol.DeploymentRecovery, error) {
	previous, err := a.store.LatestDeploymentSnapshot(ctx, snapshot.TargetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // First deployment, or a legacy release without a snapshot.
	}
	if err != nil {
		return nil, err
	}
	if previous.ServerID != snapshot.ServerID || previous.ReleaseRoot != snapshot.ReleaseRoot {
		return nil, nil // The earlier release belongs to a different deployment root.
	}
	environment, err := a.deploymentEnvironmentForSnapshot(ctx, previous)
	if err != nil {
		return nil, err
	}
	releaseID := previous.DeploymentID
	if previous.RollbackOfDeploymentID != "" {
		releaseID = previous.RollbackOfDeploymentID
	}
	publicURL := previous.ConfiguredPublicURL
	if publicURL == "" {
		publicURL = previous.DetectedPublicURL
	}
	return &protocol.DeploymentRecovery{ReleaseDeploymentID: releaseID, ComposeFile: previous.ComposeFile, WorkingDir: previous.WorkingDir, PublicURL: publicURL, Environment: environment}, nil
}

var errDeploymentSecretsUndecryptable = errors.New("deployment secrets could not be decrypted")

func (a *API) deploymentEnvironmentForSnapshot(ctx context.Context, snapshot store.DeploymentSnapshot) (map[string]string, error) {
	if snapshot.SecretSetID == "" {
		return map[string]string{}, nil
	}
	if snapshot.SecretSetKeyVersion <= 0 || snapshot.SecretSetUpdatedAt == nil {
		return nil, store.ErrDeploymentSnapshotUnavailable
	}
	ciphertext, updatedAt, err := a.store.SecretCiphertextVersion(ctx, snapshot.SecretSetID, snapshot.SecretSetKeyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrDeploymentSnapshotUnavailable
	}
	if err != nil {
		return nil, err
	}
	if !snapshot.SecretSetUpdatedAt.UTC().Equal(updatedAt.UTC()) {
		return nil, store.ErrDeploymentSnapshotUnavailable
	}
	environment := map[string]string{}
	if err := a.vault.Decrypt(ciphertext, &environment); err != nil {
		return nil, fmt.Errorf("%w: %v", errDeploymentSecretsUndecryptable, err)
	}
	return environment, nil
}

func (a *API) deploymentContainerAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(chi.URLParam(r, "action"))
	if r.Method == http.MethodDelete {
		action = "remove"
	}
	if action == "" {
		var input struct {
			Action string `json:"action"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		action = strings.TrimSpace(input.Action)
	}
	switch action {
	case "start", "stop", "restart", "remove":
	default:
		writeError(w, http.StatusBadRequest, "container action must be start, stop, restart, or remove")
		return
	}
	target, err := a.store.DeploymentTarget(r.Context(), chi.URLParam(r, "targetID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "deployment target not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load deployment target")
		return
	}
	server, err := a.store.Server(r.Context(), target.ServerID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "target server must be online before changing containers")
		return
	}
	environment, err := a.deploymentEnvironment(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not decrypt deployment secrets")
		return
	}
	command := protocol.ContainerActionCommand{TargetID: target.ID, Action: action, PublicURL: target.ConfiguredPublicURL, ReleaseRoot: target.ReleaseRoot, ComposeFile: target.ComposeFile, WorkingDir: target.WorkingDir, Environment: environment}
	ciphertext, err := a.vault.Encrypt(command)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not protect container operation")
		return
	}
	idempotency := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotency == "" {
		idempotency = "container:" + target.ID + ":" + action + ":" + store.NewID()
	} else if len(idempotency) > 200 || containsControlRune(idempotency) {
		writeError(w, http.StatusBadRequest, "invalid Idempotency-Key")
		return
	} else {
		idempotency = "container:" + target.ID + ":" + action + ":" + idempotency
	}
	operationID, err := a.store.QueueDeploymentContainerOperation(r.Context(), target.ID, target.ServerID, action, ciphertext, idempotency)
	if errors.Is(err, store.ErrDeploymentActive) || errors.Is(err, store.ErrDeploymentContainerActive) {
		writeError(w, http.StatusConflict, "another deployment or container operation is already active")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not queue container operation")
		return
	}
	a.gateway.Wake(target.ServerID)
	_ = a.store.Audit(r.Context(), currentSession(r).UserID, "deployment.container."+action, "deployment_target", target.ID, map[string]string{"operation_id": operationID, "project_id": target.ProjectID, "environment": target.Environment}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID, "target_id": target.ID, "action": action})
}
