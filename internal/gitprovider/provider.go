package gitprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Request contains only non-secret repository metadata. Credentials are
// supplied separately so they can never be persisted in an operation payload.
type Request struct {
	ProjectID  string
	Provider   string
	Endpoint   string
	Token      string
	Username   string
	Namespace  string
	Repository string
	Visibility string
}

type Result struct {
	Provider   string `json:"provider"`
	Namespace  string `json:"namespace,omitempty"`
	Repository string `json:"repository"`
	FetchURL   string `json:"fetch_url"`
	PushURL    string `json:"push_url"`
	WebURL     string `json:"web_url,omitempty"`
}

type Client struct {
	HTTPClient *http.Client
}

type Creator interface {
	Create(context.Context, Request) (Result, error)
}

func (c Client) Create(ctx context.Context, request Request) (Result, error) {
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	request.Endpoint = strings.TrimRight(strings.TrimSpace(request.Endpoint), "/")
	request.Namespace = strings.Trim(strings.TrimSpace(request.Namespace), "/")
	request.Repository = strings.TrimSpace(request.Repository)
	request.Token = strings.TrimSpace(request.Token)
	if request.ProjectID == "" || request.Provider == "" || request.Endpoint == "" || request.Token == "" || request.Repository == "" {
		return Result{}, errors.New("provider request is incomplete")
	}
	if request.Provider != "gitee" && request.Provider != "github" && request.Provider != "gitlab" {
		return Result{}, fmt.Errorf("unsupported Git provider %q", request.Provider)
	}
	parsed, err := url.Parse(request.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Result{}, errors.New("provider endpoint is invalid")
	}
	if strings.ContainsAny(request.Repository, "/\\\x00\r\n") || strings.HasPrefix(request.Repository, ".") || request.Repository == "" {
		return Result{}, errors.New("repository name is invalid")
	}
	if request.Visibility == "" {
		request.Visibility = "private"
	}
	if request.Visibility != "private" && request.Visibility != "internal" && request.Visibility != "public" {
		return Result{}, errors.New("repository visibility is invalid")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	switch request.Provider {
	case "gitee":
		return createGitee(ctx, client, request)
	case "github":
		return createGitHub(ctx, client, request)
	default:
		return createGitLab(ctx, client, request)
	}
}

const markerPrefix = "wio-project:"

func createGitee(ctx context.Context, client *http.Client, r Request) (Result, error) {
	base := r.Endpoint + "/api/v5"
	path := "/user/repos"
	if r.Namespace != "" {
		path = "/orgs/" + url.PathEscape(r.Namespace) + "/repos"
	}
	body := map[string]any{"name": r.Repository, "private": r.Visibility != "public", "description": markerPrefix + r.ProjectID}
	var response struct {
		HTMLURL  string `json:"html_url"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
		Path     string `json:"path"`
	}
	if err := requestJSON(ctx, client, http.MethodPost, base+path, r.Token, body, &response); err != nil {
		if isConflict(err) {
			return lookupGitee(ctx, client, r, base)
		}
		return Result{}, err
	}
	fetch := response.CloneURL
	if fetch == "" {
		fetch = response.SSHURL
	}
	if response.Path == "" {
		response.Path = r.Repository
	}
	if fetch == "" {
		fetch = strings.TrimRight(r.Endpoint, "/") + "/" + strings.Trim(r.Namespace+"/"+r.Repository, "/") + ".git"
	}
	if fetch == "" {
		return Result{}, errors.New("provider did not return a clone URL")
	}
	return Result{Provider: r.Provider, Namespace: r.Namespace, Repository: r.Repository, FetchURL: fetch, PushURL: fetch, WebURL: response.HTMLURL}, nil
}

func createGitHub(ctx context.Context, client *http.Client, r Request) (Result, error) {
	base := r.Endpoint + "/api/v3"
	if parsed, _ := url.Parse(r.Endpoint); parsed != nil {
		switch strings.ToLower(parsed.Host) {
		case "github.com":
			base = "https://api.github.com"
		case "api.github.com":
			base = r.Endpoint
		}
	}
	path := "/user/repos"
	if r.Namespace != "" {
		path = "/orgs/" + url.PathEscape(r.Namespace) + "/repos"
	}
	body := map[string]any{"name": r.Repository, "private": r.Visibility != "public", "description": markerPrefix + r.ProjectID}
	var response struct {
		HTMLURL  string `json:"html_url"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
	}
	if err := requestJSON(ctx, client, http.MethodPost, base+path, r.Token, body, &response); err != nil {
		if isConflict(err) {
			return lookupGitHub(ctx, client, r, base)
		}
		return Result{}, err
	}
	fetch := response.CloneURL
	if fetch == "" {
		fetch = response.SSHURL
	}
	if fetch == "" {
		return Result{}, errors.New("provider did not return a clone URL")
	}
	return Result{Provider: r.Provider, Namespace: r.Namespace, Repository: r.Repository, FetchURL: fetch, PushURL: fetch, WebURL: response.HTMLURL}, nil
}

func createGitLab(ctx context.Context, client *http.Client, r Request) (Result, error) {
	base := r.Endpoint + "/api/v4"
	body := map[string]any{"name": r.Repository, "path": r.Repository, "visibility": r.Visibility, "description": markerPrefix + r.ProjectID}
	if r.Namespace != "" {
		namespaceID := r.Namespace
		if _, err := strconv.Atoi(namespaceID); err != nil {
			resolved, resolveErr := resolveGitLabNamespace(ctx, client, r, base)
			if resolveErr != nil {
				return Result{}, resolveErr
			}
			namespaceID = resolved
		}
		namespaceNumber, err := strconv.Atoi(namespaceID)
		if err != nil {
			return Result{}, errors.New("GitLab namespace ID is invalid")
		}
		body["namespace_id"] = namespaceNumber
	}
	var response struct {
		HTTPURL string `json:"http_url_to_repo"`
		SSHURL  string `json:"ssh_url_to_repo"`
		Path    string `json:"path"`
		WebURL  string `json:"web_url"`
	}
	if err := requestJSON(ctx, client, http.MethodPost, base+"/projects", r.Token, body, &response); err != nil {
		if isConflict(err) {
			return lookupGitLab(ctx, client, r, base)
		}
		return Result{}, err
	}
	fetch := response.HTTPURL
	if fetch == "" {
		fetch = response.SSHURL
	}
	if fetch == "" {
		return Result{}, errors.New("provider did not return a clone URL")
	}
	if response.Path == "" {
		response.Path = r.Repository
	}
	return Result{Provider: r.Provider, Namespace: r.Namespace, Repository: response.Path, FetchURL: fetch, PushURL: fetch, WebURL: response.WebURL}, nil
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d: %s", e.StatusCode, e.Message)
}

func isConflict(err error) bool {
	httpErr, ok := err.(*HTTPError)
	return ok && (httpErr.StatusCode == http.StatusConflict || httpErr.StatusCode == http.StatusUnprocessableEntity)
}

func lookupGitee(ctx context.Context, client *http.Client, r Request, base string) (Result, error) {
	owner, err := providerOwner(ctx, client, r, base+"/user")
	if err != nil {
		return Result{}, err
	}
	var response struct {
		Description string `json:"description"`
		HTMLURL     string `json:"html_url"`
		CloneURL    string `json:"clone_url"`
		SSHURL      string `json:"ssh_url"`
		Path        string `json:"path"`
	}
	if err := requestJSON(ctx, client, http.MethodGet, base+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(r.Repository), r.Token, nil, &response); err != nil {
		return Result{}, err
	}
	if response.Description != markerPrefix+r.ProjectID {
		return Result{}, errors.New("remote repository already exists and is not owned by this project")
	}
	fetch := response.CloneURL
	if fetch == "" {
		fetch = response.SSHURL
	}
	if fetch == "" {
		return Result{}, errors.New("provider did not return a clone URL")
	}
	return Result{Provider: r.Provider, Namespace: owner, Repository: r.Repository, FetchURL: fetch, PushURL: fetch, WebURL: response.HTMLURL}, nil
}

func lookupGitHub(ctx context.Context, client *http.Client, r Request, base string) (Result, error) {
	owner, err := providerOwner(ctx, client, r, base+"/user")
	if err != nil {
		return Result{}, err
	}
	var response struct {
		Description string `json:"description"`
		HTMLURL     string `json:"html_url"`
		CloneURL    string `json:"clone_url"`
		SSHURL      string `json:"ssh_url"`
	}
	if err := requestJSON(ctx, client, http.MethodGet, base+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(r.Repository), r.Token, nil, &response); err != nil {
		return Result{}, err
	}
	if response.Description != markerPrefix+r.ProjectID {
		return Result{}, errors.New("remote repository already exists and is not owned by this project")
	}
	fetch := response.CloneURL
	if fetch == "" {
		fetch = response.SSHURL
	}
	if fetch == "" {
		return Result{}, errors.New("provider did not return a clone URL")
	}
	return Result{Provider: r.Provider, Namespace: owner, Repository: r.Repository, FetchURL: fetch, PushURL: fetch, WebURL: response.HTMLURL}, nil
}

func lookupGitLab(ctx context.Context, client *http.Client, r Request, base string) (Result, error) {
	owner := r.Namespace
	if owner == "" {
		var user struct {
			Username string `json:"username"`
		}
		if err := requestJSON(ctx, client, http.MethodGet, base+"/user", r.Token, nil, &user); err != nil {
			return Result{}, err
		}
		owner = user.Username
	}
	var response struct {
		Description string `json:"description"`
		HTTPURL     string `json:"http_url_to_repo"`
		SSHURL      string `json:"ssh_url_to_repo"`
		Path        string `json:"path"`
		WebURL      string `json:"web_url"`
	}
	if err := requestJSON(ctx, client, http.MethodGet, base+"/projects/"+url.PathEscape(owner+"/"+r.Repository), r.Token, nil, &response); err != nil {
		return Result{}, err
	}
	if response.Description != markerPrefix+r.ProjectID {
		return Result{}, errors.New("remote repository already exists and is not owned by this project")
	}
	fetch := response.HTTPURL
	if fetch == "" {
		fetch = response.SSHURL
	}
	if fetch == "" {
		return Result{}, errors.New("provider did not return a clone URL")
	}
	if response.Path == "" {
		response.Path = r.Repository
	}
	return Result{Provider: r.Provider, Namespace: owner, Repository: response.Path, FetchURL: fetch, PushURL: fetch, WebURL: response.WebURL}, nil
}

func providerOwner(ctx context.Context, client *http.Client, r Request, endpoint string) (string, error) {
	if r.Namespace != "" {
		return r.Namespace, nil
	}
	var user struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	}
	if err := requestJSON(ctx, client, http.MethodGet, endpoint, r.Token, nil, &user); err != nil {
		return "", err
	}
	if user.Login != "" {
		return user.Login, nil
	}
	if user.Username != "" {
		return user.Username, nil
	}
	return "", errors.New("provider user identity is unavailable")
}

func resolveGitLabNamespace(ctx context.Context, client *http.Client, r Request, base string) (string, error) {
	var namespaces []struct {
		ID       int    `json:"id"`
		Path     string `json:"path"`
		FullPath string `json:"full_path"`
	}
	endpoint := base + "/namespaces?search=" + url.QueryEscape(r.Namespace) + "&per_page=100"
	if err := requestJSON(ctx, client, http.MethodGet, endpoint, r.Token, nil, &namespaces); err != nil {
		return "", err
	}
	for _, item := range namespaces {
		if item.FullPath == r.Namespace || item.Path == r.Namespace {
			return strconv.Itoa(item.ID), nil
		}
	}
	return "", errors.New("GitLab namespace was not found")
}

func requestJSON(ctx context.Context, client *http.Client, method, endpoint, token string, body any, out any) error {
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("provider request failed: %w", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		message := strings.TrimSpace(problem.Message)
		if message == "" {
			message = strings.TrimSpace(problem.Error)
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: message}
	}
	if out != nil && len(data) > 0 && json.Unmarshal(data, out) != nil {
		return errors.New("provider returned invalid JSON")
	}
	return nil
}
