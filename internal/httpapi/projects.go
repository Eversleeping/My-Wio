package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/wio-platform/wio/internal/gitprovider"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/store"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (a *API) projects(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	projects, err := a.store.ListProjectsFiltered(r.Context(), query.Get("name"), query.Get("server_id"), query.Get("status"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list projects")
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (a *API) projectDetail(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	project, err := a.store.Project(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	remotes, err := a.store.ProjectRemotes(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project remotes")
		return
	}
	operations, err := a.store.ListProjectOperations(r.Context(), projectID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project operations")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "remotes": remotes, "operations": operations})
}

func (a *API) projectOperations(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if _, err := a.store.Project(r.Context(), projectID); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	operations, err := a.store.ListProjectOperations(r.Context(), projectID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project operations")
		return
	}
	writeJSON(w, http.StatusOK, operations)
}

type createProjectInput struct {
	Mode             string `json:"mode"`
	Name             string `json:"name"`
	ServerID         string `json:"server_id"`
	Destination      string `json:"destination"`
	InitialBranch    string `json:"initial_branch"`
	InitializeREADME bool   `json:"initialize_readme"`
	Remote           struct {
		Mode       string `json:"mode"`
		URL        string `json:"url"`
		Provider   string `json:"provider"`
		Namespace  string `json:"namespace"`
		Repository string `json:"repository"`
		Visibility string `json:"visibility"`
	} `json:"remote"`
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var input createProjectInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Mode = strings.TrimSpace(input.Mode)
	input.Name = strings.TrimSpace(input.Name)
	input.ServerID = strings.TrimSpace(input.ServerID)
	input.Destination = strings.TrimSpace(input.Destination)
	input.InitialBranch = strings.TrimSpace(input.InitialBranch)
	input.Remote.Mode = strings.TrimSpace(input.Remote.Mode)
	input.Remote.URL = strings.TrimSpace(input.Remote.URL)
	input.Remote.Provider = strings.ToLower(strings.TrimSpace(input.Remote.Provider))
	input.Remote.Namespace = strings.Trim(strings.TrimSpace(input.Remote.Namespace), "/")
	input.Remote.Repository = strings.TrimSpace(input.Remote.Repository)
	input.Remote.Visibility = strings.ToLower(strings.TrimSpace(input.Remote.Visibility))
	if input.Mode != "blank" {
		writeError(w, http.StatusBadRequest, "mode must be blank")
		return
	}
	if input.Name == "" || input.ServerID == "" {
		writeError(w, http.StatusBadRequest, "name and server_id are required")
		return
	}
	if utf8.RuneCountInString(input.Name) > projectNameLimit {
		writeError(w, http.StatusBadRequest, "project name is too long")
		return
	}
	if input.InitialBranch == "" {
		input.InitialBranch = "main"
	}
	if len(input.InitialBranch) > gitBranchNameLimit || strings.ContainsAny(input.InitialBranch, "\x00\r\n\t ") || strings.HasPrefix(input.InitialBranch, "-") {
		writeError(w, http.StatusBadRequest, "initial_branch is invalid")
		return
	}
	if len(input.Destination) > projectDestinationLimit || strings.ContainsAny(input.Destination, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "destination is invalid")
		return
	}
	if input.Remote.Mode == "" {
		input.Remote.Mode = "none"
	}
	if err := validateBlankProjectRemote(input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	server, err := a.store.Server(r.Context(), input.ServerID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	if (input.InitializeREADME || input.Remote.Mode == "create") && server.GitProfileID == "" {
		writeError(w, http.StatusConflict, "a Git credential profile is required for this operation")
		return
	}
	if input.InitializeREADME {
		profile, profileErr := a.store.CredentialProfile(r.Context(), server.GitProfileID)
		if profileErr != nil || profile.CommitName == "" || profile.CommitEmail == "" {
			writeError(w, http.StatusConflict, "a Git credential profile with commit identity is required for the initial commit")
			return
		}
	}
	var provision store.BlankProjectProvision
	switch input.Remote.Mode {
	case "existing":
		provision, err = a.store.CreateBlankProjectWithRemote(r.Context(), server.ID, input.Name, input.Destination, input.InitialBranch, input.InitializeREADME, store.BlankProjectRemoteSpec{Mode: "existing", URL: input.Remote.URL})
	case "create":
		provision, err = a.store.PrepareBlankProject(r.Context(), server.ID, input.Name, input.Destination, input.InitialBranch, input.InitializeREADME, store.BlankProjectRemoteSpec{Mode: "create", Provider: input.Remote.Provider, Namespace: input.Remote.Namespace, Repository: input.Remote.Repository, Visibility: input.Remote.Visibility, CredentialProfileID: server.GitProfileID})
	default:
		provision, err = a.store.CreateBlankProject(r.Context(), server.ID, input.Name, input.Destination, input.InitialBranch, input.InitializeREADME)
	}
	if err != nil {
		if databaseConflict(err) {
			writeError(w, http.StatusConflict, "project could not be reserved")
		} else {
			writeError(w, http.StatusInternalServerError, "could not create blank project")
		}
		return
	}
	if input.Remote.Mode == "create" {
		provision, err = a.provisionProjectRemote(r.Context(), provision)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.create", "project", provision.Project.ID, map[string]any{
		"server_id": server.ID, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID,
		"initial_branch": input.InitialBranch, "initialize_readme": input.InitializeREADME, "remote_mode": input.Remote.Mode,
	}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"project": provision.Project, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID})
}

func validateBlankProjectRemote(input createProjectInput) error {
	switch input.Remote.Mode {
	case "none":
		return nil
	case "existing":
		if input.Remote.URL == "" {
			return errors.New("remote URL is required")
		}
		if strings.ContainsAny(input.Remote.URL, "\x00\r\n\t ") {
			return errors.New("remote URL contains invalid characters")
		}
		if err := gitRemoteCredentialError(input.Remote.URL); err != nil {
			return err
		}
		if validSCPRemote(input.Remote.URL) {
			return nil
		}
		parsed, err := url.Parse(input.Remote.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") {
			return errors.New("remote URL must be an HTTPS or SSH URL")
		}
		return nil
	case "create":
		if input.Remote.Provider != "gitee" && input.Remote.Provider != "github" && input.Remote.Provider != "gitlab" {
			return errors.New("Git provider must be Gitee, GitHub, or GitLab")
		}
		if input.Remote.Repository == "" || len(input.Remote.Repository) > 200 || strings.ContainsAny(input.Remote.Repository, "/\\\x00\r\n") {
			return errors.New("remote repository name is invalid")
		}
		if input.Remote.Visibility == "" {
			return errors.New("remote visibility is required")
		}
		if input.Remote.Visibility != "private" && input.Remote.Visibility != "internal" && input.Remote.Visibility != "public" {
			return errors.New("remote visibility is invalid")
		}
		if len(input.Remote.Namespace) > 200 || strings.ContainsAny(input.Remote.Namespace, "\\\x00\r\n") {
			return errors.New("remote namespace is invalid")
		}
		return nil
	default:
		return errors.New("remote mode must be none, existing, or create")
	}
}

func validSCPRemote(raw string) bool {
	at := strings.LastIndex(raw, "@")
	colon := strings.Index(raw, ":")
	return at > 0 && colon > at+1 && colon < len(raw)-1 && !strings.ContainsAny(raw[:at], "/\\:@") && !strings.ContainsAny(raw[at+1:colon], "/\\:@") && !strings.HasPrefix(raw[colon+1:], "-")
}

func (a *API) provisionProjectRemote(ctx context.Context, provision store.BlankProjectProvision) (store.BlankProjectProvision, error) {
	operation, err := a.store.Operation(ctx, provision.OperationID)
	if err != nil {
		return store.BlankProjectProvision{}, errors.New("could not load remote provisioning operation")
	}
	remote, err := a.store.ProjectRemote(ctx, provision.Project.ID, "origin")
	if err != nil {
		return store.BlankProjectProvision{}, errors.New("could not load remote provisioning request")
	}
	profile, err := a.store.CredentialProfile(ctx, remote.CredentialProfileID)
	if err != nil || profile.Kind != "git" {
		message := "Git credential profile is unavailable"
		_ = a.store.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: message}, "failed")
		return store.BlankProjectProvision{}, errors.New(message)
	}
	if a.vault == nil {
		message := "credential vault is unavailable"
		_ = a.store.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: message}, "failed")
		return store.BlankProjectProvision{}, errors.New(message)
	}
	var token string
	if err := a.vault.Decrypt(profile.Ciphertext, &token); err != nil || strings.TrimSpace(token) == "" {
		message := "Git credential could not be decrypted"
		_ = a.store.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: message}, "failed")
		return store.BlankProjectProvision{}, errors.New(message)
	}
	creator := a.gitProviders
	if creator == nil {
		creator = gitprovider.Client{}
	}
	created, err := creator.Create(ctx, gitprovider.Request{ProjectID: provision.Project.ID, Provider: remote.Provider, Endpoint: profile.Endpoint, Token: token, Username: profile.Username, Namespace: remote.Namespace, Repository: remote.Repository, Visibility: remote.Visibility})
	if err != nil {
		message := "remote repository creation failed"
		_ = a.store.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: message}, "failed")
		return store.BlankProjectProvision{}, errors.New(message)
	}
	remoteResult := protocol.ProjectRemoteResult{Provider: created.Provider, Namespace: created.Namespace, Repository: created.Repository, FetchURL: created.FetchURL, PushURL: created.PushURL, WebURL: created.WebURL}
	if err := a.store.UpdateProjectRemote(ctx, provision.Project.ID, remoteResult); err != nil {
		message := "remote repository was created but its metadata could not be persisted"
		_ = a.store.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: message}, "partial")
		return store.BlankProjectProvision{}, errors.New(message)
	}
	activated, err := a.store.ActivateBlankProjectRemote(ctx, provision.Project.ID, provision.OperationID, remoteResult)
	if err != nil {
		message := "remote repository was created but local project activation failed"
		_ = a.store.FailBlankProject(ctx, operation, provision.Command, protocol.OperationResult{OperationID: operation.ID, Status: "failed", Message: message}, "partial")
		return store.BlankProjectProvision{}, errors.New(message)
	}
	return activated, nil
}

func (a *API) retryProjectCreation(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	project, err := a.store.Project(r.Context(), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project")
		return
	}
	if project.Status != "failed" && project.Status != "partial" {
		writeError(w, http.StatusConflict, "only a failed or partial blank project can be retried")
		return
	}
	operations, err := a.store.ListProjectOperations(r.Context(), projectID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project operations")
		return
	}
	serverID := ""
	for _, operation := range operations {
		if operation.Kind == protocol.OpGitProjectCreate {
			serverID = operation.ServerID
			break
		}
	}
	if serverID == "" {
		writeError(w, http.StatusConflict, "blank project creation operation was not found")
		return
	}
	server, err := a.store.Server(r.Context(), serverID)
	if err != nil {
		writeStoreError(w, err, "could not load server")
		return
	}
	if server.Status != "online" {
		writeError(w, http.StatusConflict, "server is offline")
		return
	}
	provision, err := a.store.RetryBlankProject(r.Context(), projectID)
	if err != nil {
		writeStoreError(w, err, "could not retry blank project")
		return
	}
	operation, operationErr := a.store.Operation(r.Context(), provision.OperationID)
	if operationErr != nil {
		writeError(w, http.StatusInternalServerError, "could not load retry operation")
		return
	}
	if operation.Status == "preparing" {
		provision, err = a.provisionProjectRemote(r.Context(), provision)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	a.gateway.Wake(server.ID)
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.create.retry", "project", projectID, map[string]string{"server_id": server.ID, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID}, clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"project": provision.Project, "workspace_id": provision.WorkspaceID, "operation_id": provision.OperationID})
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerID      *string `json:"server_id"`
		Name          *string `json:"name"`
		Description   *string `json:"description"`
		DefaultBranch *string `json:"default_branch"`
		Pinned        *bool   `json:"pinned"`
		Hidden        *bool   `json:"hidden"`
		Archived      *bool   `json:"archived"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Name == nil && input.Description == nil && input.DefaultBranch == nil && input.Pinned == nil && input.Hidden == nil && input.Archived == nil {
		writeError(w, http.StatusBadRequest, "at least one project field is required")
		return
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "project name is required")
			return
		}
		if utf8.RuneCountInString(name) > projectNameLimit {
			writeError(w, http.StatusBadRequest, "project name is too long")
			return
		}
		input.Name = &name
	}
	if input.Description != nil {
		description := strings.TrimSpace(*input.Description)
		if utf8.RuneCountInString(description) > 4096 {
			writeError(w, http.StatusBadRequest, "project description is too long")
			return
		}
		input.Description = &description
	}
	if input.DefaultBranch != nil {
		branch := strings.TrimSpace(*input.DefaultBranch)
		if branch == "" || len(branch) > gitBranchNameLimit || strings.ContainsAny(branch, "\x00\r\n\t ") || strings.HasPrefix(branch, "-") {
			writeError(w, http.StatusBadRequest, "default_branch is invalid")
			return
		}
		input.DefaultBranch = &branch
	}
	projectID := chi.URLParam(r, "projectID")
	var project store.Project
	var err error
	if input.ServerID != nil {
		serverID := strings.TrimSpace(*input.ServerID)
		if serverID == "" || input.Description != nil || input.DefaultBranch != nil || input.Archived != nil {
			writeError(w, http.StatusBadRequest, "server project preferences require server_id and only support name, pinned and hidden")
			return
		}
		input.ServerID = &serverID
		project, err = a.store.UpdateProjectServerPreferences(r.Context(), projectID, serverID, input.Name, input.Pinned, input.Hidden)
	} else {
		project, err = a.store.UpdateProjectDetails(r.Context(), projectID, input.Name, input.Description, input.DefaultBranch, input.Pinned, input.Hidden, input.Archived)
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update project")
		return
	}
	detail := map[string]any{}
	if input.ServerID != nil {
		detail["server_id"] = *input.ServerID
	}
	if input.Name != nil {
		detail["name"] = *input.Name
	}
	if input.Description != nil {
		detail["description"] = *input.Description
	}
	if input.DefaultBranch != nil {
		detail["default_branch"] = *input.DefaultBranch
	}
	if input.Pinned != nil {
		detail["pinned"] = *input.Pinned
	}
	if input.Hidden != nil {
		detail["hidden"] = *input.Hidden
	}
	if input.Archived != nil {
		detail["archived"] = *input.Archived
	}
	session := currentSession(r)
	_ = a.store.Audit(r.Context(), session.UserID, "project.update", "project", project.ID, detail, clientIP(r))
	writeJSON(w, http.StatusOK, project)
}
