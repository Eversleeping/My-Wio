package gitrepository

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultInitialBranch = "main"
	defaultCommitLimit   = 50
	maxCommitLimit       = 200
)

// CreateOptions describes a new repository that will be created below a managed root.
type CreateOptions struct {
	ProjectID        string
	Path             string
	InitialBranch    string
	RemoteURL        string
	InitializeREADME bool
	READMEContent    string
	CommitMessage    string
	AuthorName       string
	AuthorEmail      string
}

type CreateResult struct {
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	Head      string `json:"head,omitempty"`
	Unborn    bool   `json:"unborn"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type Status struct {
	Branch    string `json:"branch,omitempty"`
	Detached  bool   `json:"detached"`
	Unborn    bool   `json:"unborn"`
	Head      string `json:"head,omitempty"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
	Dirty     bool   `json:"dirty"`
}

type Branch struct {
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	Kind      string `json:"kind"`
	CommitSHA string `json:"commit_sha"`
	Upstream  string `json:"upstream,omitempty"`
	Current   bool   `json:"current"`
}

type Remote struct {
	Name      string   `json:"name"`
	FetchURLs []string `json:"fetch_urls"`
	PushURLs  []string `json:"push_urls"`
}

type CommitLogOptions struct {
	Ref    string
	Offset int
	Limit  int
}

type Commit struct {
	SHA         string    `json:"sha"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	AuthoredAt  time.Time `json:"authored_at"`
	Title       string    `json:"title"`
	Parents     []string  `json:"parents"`
}

type CommitPage struct {
	Commits    []Commit `json:"commits"`
	Offset     int      `json:"offset"`
	Limit      int      `json:"limit"`
	HasMore    bool     `json:"has_more"`
	NextOffset int      `json:"next_offset,omitempty"`
}

func Create(ctx context.Context, options CreateOptions, managedRoots []string) (result CreateResult, err error) {
	projectID := strings.TrimSpace(options.ProjectID)
	if projectID == "" {
		return CreateResult{}, errors.New("project ID is required")
	}
	if containsControl(projectID) {
		return CreateResult{}, errors.New("project ID contains control characters")
	}
	branch := strings.TrimSpace(options.InitialBranch)
	if branch == "" {
		branch = defaultInitialBranch
	}
	if output, commandErr := runGit(ctx, "", "check-ref-format", "--branch", branch); commandErr != nil {
		return CreateResult{}, fmt.Errorf("invalid initial branch: %s", cleanGitError(output, commandErr))
	}
	remoteURL := strings.TrimSpace(options.RemoteURL)
	if remoteURL != "" {
		if err := validateRemoteURL(remoteURL); err != nil {
			return CreateResult{}, err
		}
	}
	if options.InitializeREADME {
		if err := validateCommitIdentity(options); err != nil {
			return CreateResult{}, err
		}
	}
	if _, statErr := os.Lstat(strings.TrimSpace(options.Path)); statErr == nil {
		return recoverCreatedRepository(ctx, options, branch, remoteURL, managedRoots)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return CreateResult{}, fmt.Errorf("inspect repository path: %w", statErr)
	}
	target, err := allowedNewPath(options.Path, managedRoots)
	if err != nil {
		return CreateResult{}, fmt.Errorf("invalid repository path: %w", err)
	}

	if err := os.Mkdir(target, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) {
			return CreateResult{}, errors.New("repository path already exists")
		}
		return CreateResult{}, fmt.Errorf("create repository directory: %w", err)
	}
	created := true
	defer func() {
		if err != nil && created {
			_ = os.RemoveAll(target)
		}
	}()

	resolvedTarget, err := allowedExistingPath(target, managedRoots)
	if err != nil {
		return CreateResult{}, fmt.Errorf("verify repository path: %w", err)
	}
	target = resolvedTarget
	if output, commandErr := runGit(ctx, target, "init", "-b", branch); commandErr != nil {
		return CreateResult{}, fmt.Errorf("initialize repository: %s", cleanGitError(output, commandErr))
	}
	if output, commandErr := runGit(ctx, target, "config", "--local", "wio.projectId", projectID); commandErr != nil {
		return CreateResult{}, fmt.Errorf("mark repository project: %s", cleanGitError(output, commandErr))
	}
	if remoteURL != "" {
		if output, commandErr := runGit(ctx, target, "remote", "add", "--", "origin", remoteURL); commandErr != nil {
			return CreateResult{}, fmt.Errorf("add origin remote: %s", cleanGitError(output, commandErr))
		}
	}
	if options.InitializeREADME {
		if err := commitREADME(ctx, target, options); err != nil {
			return CreateResult{}, err
		}
	}

	status, err := getStatus(ctx, target)
	if err != nil {
		return CreateResult{}, err
	}
	created = false
	return CreateResult{
		Path:      target,
		Branch:    status.Branch,
		Head:      status.Head,
		Unborn:    status.Unborn,
		RemoteURL: remoteURL,
	}, nil
}

func recoverCreatedRepository(ctx context.Context, options CreateOptions, branch, remoteURL string, managedRoots []string) (CreateResult, error) {
	repositoryPath, err := resolveRepository(ctx, options.Path, managedRoots)
	if err != nil {
		return CreateResult{}, fmt.Errorf("repository path already exists: %w", err)
	}
	markerOutput, markerErr := runGit(ctx, repositoryPath, "config", "--local", "--get", "wio.projectId")
	if markerErr != nil || strings.TrimSpace(string(markerOutput)) != strings.TrimSpace(options.ProjectID) {
		return CreateResult{}, errors.New("repository path already exists for a different project")
	}
	status, err := getStatus(ctx, repositoryPath)
	if err != nil {
		return CreateResult{}, err
	}
	if status.Branch != branch {
		return CreateResult{}, errors.New("existing repository branch does not match the requested initial branch")
	}
	existingRemote := ""
	if output, remoteErr := runGit(ctx, repositoryPath, "remote", "get-url", "origin"); remoteErr == nil {
		existingRemote = strings.TrimSpace(string(output))
	}
	if remoteURL == "" && existingRemote != "" {
		return CreateResult{}, errors.New("existing repository has an unexpected origin remote")
	}
	if remoteURL != "" {
		if existingRemote == "" {
			if output, commandErr := runGit(ctx, repositoryPath, "remote", "add", "--", "origin", remoteURL); commandErr != nil {
				return CreateResult{}, fmt.Errorf("resume origin remote: %s", cleanGitError(output, commandErr))
			}
		} else if existingRemote != remoteURL {
			return CreateResult{}, errors.New("existing repository origin does not match the requested remote")
		}
	}
	if options.InitializeREADME && status.Unborn {
		if err := commitREADME(ctx, repositoryPath, options); err != nil {
			return CreateResult{}, err
		}
		status, err = getStatus(ctx, repositoryPath)
		if err != nil {
			return CreateResult{}, err
		}
	}
	if options.InitializeREADME && status.Unborn {
		return CreateResult{}, errors.New("existing repository is missing the requested initial commit")
	}
	return CreateResult{
		Path:      repositoryPath,
		Branch:    status.Branch,
		Head:      status.Head,
		Unborn:    status.Unborn,
		RemoteURL: remoteURL,
	}, nil
}

func commitREADME(ctx context.Context, repositoryPath string, options CreateOptions) error {
	if err := validateConfiguredCommitIdentity(ctx, repositoryPath, options); err != nil {
		return err
	}
	content := options.READMEContent
	if content == "" {
		content = "# " + filepath.Base(repositoryPath) + "\n"
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write README: %w", err)
	}
	if output, commandErr := runGit(ctx, repositoryPath, "add", "--", "README.md"); commandErr != nil {
		return fmt.Errorf("stage README: %s", cleanGitError(output, commandErr))
	}
	message := strings.TrimSpace(options.CommitMessage)
	if message == "" {
		message = "Initial commit"
	}
	args := []string{"-c", "commit.gpgsign=false", "commit", "--no-gpg-sign", "--no-verify", "-m", message}
	if name := strings.TrimSpace(options.AuthorName); name != "" {
		args = append([]string{"-c", "user.name=" + name}, args...)
	}
	if email := strings.TrimSpace(options.AuthorEmail); email != "" {
		args = append([]string{"-c", "user.email=" + email}, args...)
	}
	if output, commandErr := runGit(ctx, repositoryPath, args...); commandErr != nil {
		return fmt.Errorf("commit README: %s", cleanGitError(output, commandErr))
	}
	return nil
}

func GetStatus(ctx context.Context, repositoryPath string, managedRoots []string) (Status, error) {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return Status{}, err
	}
	return getStatus(ctx, repositoryPath)
}

func getStatus(ctx context.Context, repositoryPath string) (Status, error) {
	output, err := runGit(ctx, repositoryPath, "status", "--porcelain=v2", "--branch", "--untracked-files=all")
	if err != nil {
		return Status{}, fmt.Errorf("read repository status: %s", cleanGitError(output, err))
	}
	return parseStatus(output)
}

func parseStatus(output []byte) (Status, error) {
	status := Status{}
	var hasOID, hasHead bool
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			hasOID = true
			value := strings.TrimSpace(strings.TrimPrefix(line, "# branch.oid "))
			if value == "(initial)" {
				status.Unborn = true
			} else if !isObjectID(value) {
				return Status{}, errors.New("parse repository status: branch OID is invalid")
			} else {
				status.Head = value
			}
		case strings.HasPrefix(line, "# branch.head "):
			hasHead = true
			value := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if value == "(detached)" {
				status.Detached = true
			} else if value == "" {
				return Status{}, errors.New("parse repository status: branch name is empty")
			} else {
				status.Branch = value
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			status.Upstream = strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream "))
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) != 2 {
				return Status{}, fmt.Errorf("parse repository status: invalid branch divergence %q", line)
			}
			var countErr error
			status.Ahead, countErr = parseSignedCount(fields[0], '+')
			if countErr != nil {
				return Status{}, fmt.Errorf("parse repository status: %w", countErr)
			}
			status.Behind, countErr = parseSignedCount(fields[1], '-')
			if countErr != nil {
				return Status{}, fmt.Errorf("parse repository status: %w", countErr)
			}
		case strings.HasPrefix(line, "? "):
			status.Untracked++
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "):
			fields := strings.Fields(line)
			if len(fields) < 2 || len(fields[1]) != 2 {
				return Status{}, fmt.Errorf("parse repository status: invalid entry %q", line)
			}
			if fields[1][0] != '.' {
				status.Staged++
			}
			if fields[1][1] != '.' {
				status.Unstaged++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Status{}, fmt.Errorf("parse repository status: %w", err)
	}
	if !hasOID || !hasHead {
		return Status{}, errors.New("parse repository status: required branch headers are missing")
	}
	if status.Unborn && status.Detached {
		return Status{}, errors.New("parse repository status: unborn branch cannot be detached")
	}
	status.Dirty = status.Staged > 0 || status.Unstaged > 0 || status.Untracked > 0
	return status, nil
}

func ListBranches(ctx context.Context, repositoryPath string, managedRoots []string) ([]Branch, error) {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return nil, err
	}
	format := "%(refname)%00%(refname:short)%00%(objectname)%00%(upstream:short)%00%(HEAD)%00%(symref)"
	output, err := runGit(ctx, repositoryPath, "for-each-ref", "--format="+format, "refs/heads", "refs/remotes")
	if err != nil {
		return nil, fmt.Errorf("list branches: %s", cleanGitError(output, err))
	}
	branches, err := parseBranches(output)
	if err != nil {
		return nil, err
	}
	for _, branch := range branches {
		if branch.Kind == "local" && branch.Current {
			return branches, nil
		}
	}
	status, err := getStatus(ctx, repositoryPath)
	if err != nil {
		return nil, err
	}
	if status.Unborn && !status.Detached && status.Branch != "" {
		branches = append(branches, Branch{
			Name:     status.Branch,
			FullName: "refs/heads/" + status.Branch,
			Kind:     "local",
			Current:  true,
		})
	}
	return branches, nil
}

func parseBranches(output []byte) ([]Branch, error) {
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	branches := make([]Branch, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{0})
		if len(fields) != 6 {
			return nil, errors.New("parse branches: unexpected Git output")
		}
		fullName := string(fields[0])
		kind := ""
		switch {
		case strings.HasPrefix(fullName, "refs/heads/"):
			kind = "local"
		case strings.HasPrefix(fullName, "refs/remotes/"):
			kind = "remote"
		default:
			continue
		}
		if len(fields[5]) != 0 {
			continue
		}
		if len(fields[1]) == 0 || len(fields[2]) == 0 {
			return nil, errors.New("parse branches: branch name and commit are required")
		}
		if !isObjectID(string(fields[2])) {
			return nil, errors.New("parse branches: branch commit is invalid")
		}
		currentMarker := string(fields[4])
		if currentMarker != "*" && currentMarker != " " {
			return nil, errors.New("parse branches: current marker is invalid")
		}
		branches = append(branches, Branch{
			Name:      string(fields[1]),
			FullName:  fullName,
			Kind:      kind,
			CommitSHA: string(fields[2]),
			Upstream:  string(fields[3]),
			Current:   currentMarker == "*",
		})
	}
	return branches, nil
}

func ListRemotes(ctx context.Context, repositoryPath string, managedRoots []string) ([]Remote, error) {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return nil, err
	}
	output, err := runGit(ctx, repositoryPath, "remote")
	if err != nil {
		return nil, fmt.Errorf("list remotes: %s", cleanGitError(output, err))
	}
	names := splitLines(output)
	remotes := make([]Remote, 0, len(names))
	for _, name := range names {
		fetchOutput, fetchErr := runGit(ctx, repositoryPath, "remote", "get-url", "--all", "--", name)
		if fetchErr != nil {
			return nil, fmt.Errorf("read fetch URLs for remote %q: %s", name, cleanGitError(fetchOutput, fetchErr))
		}
		pushOutput, pushErr := runGit(ctx, repositoryPath, "remote", "get-url", "--push", "--all", "--", name)
		if pushErr != nil {
			return nil, fmt.Errorf("read push URLs for remote %q: %s", name, cleanGitError(pushOutput, pushErr))
		}
		fetchURLs := splitLines(fetchOutput)
		pushURLs := splitLines(pushOutput)
		if len(fetchURLs) == 0 || len(pushURLs) == 0 {
			return nil, fmt.Errorf("parse remote %q: fetch and push URLs are required", name)
		}
		remotes = append(remotes, Remote{
			Name:      name,
			FetchURLs: fetchURLs,
			PushURLs:  pushURLs,
		})
	}
	return remotes, nil
}

func ListCommits(ctx context.Context, repositoryPath string, managedRoots []string, options CommitLogOptions) (CommitPage, error) {
	repositoryPath, err := resolveRepository(ctx, repositoryPath, managedRoots)
	if err != nil {
		return CommitPage{}, err
	}
	if options.Offset < 0 {
		return CommitPage{}, errors.New("commit offset cannot be negative")
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultCommitLimit
	}
	if limit < 0 || limit > maxCommitLimit {
		return CommitPage{}, fmt.Errorf("commit limit must be between 1 and %d", maxCommitLimit)
	}
	requestedRef := strings.TrimSpace(options.Ref)
	ref := requestedRef
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") || containsWhitespaceOrControl(ref) {
		return CommitPage{}, errors.New("invalid commit reference")
	}
	resolved, resolveErr := runGit(ctx, repositoryPath, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if resolveErr != nil {
		status, statusErr := getStatus(ctx, repositoryPath)
		if statusErr == nil && status.Unborn && (requestedRef == "" || requestedRef == "HEAD") {
			return CommitPage{Commits: []Commit{}, Offset: options.Offset, Limit: limit}, nil
		}
		return CommitPage{}, fmt.Errorf("resolve commit reference: %s", cleanGitError(resolved, resolveErr))
	}
	resolvedRef := strings.TrimSpace(string(resolved))
	format := "%H%x00%an%x00%ae%x00%aI%x00%s%x00%P%x00"
	args := []string{
		"log", resolvedRef,
		"--skip=" + strconv.Itoa(options.Offset),
		"-n", strconv.Itoa(limit + 1),
		"--format=" + format,
	}
	output, err := runGit(ctx, repositoryPath, args...)
	if err != nil {
		return CommitPage{}, fmt.Errorf("read commit log: %s", cleanGitError(output, err))
	}
	commits, err := parseCommits(output)
	if err != nil {
		return CommitPage{}, err
	}
	hasMore := len(commits) > limit
	if hasMore {
		commits = commits[:limit]
	}
	page := CommitPage{
		Commits: commits,
		Offset:  options.Offset,
		Limit:   limit,
		HasMore: hasMore,
	}
	if hasMore {
		page.NextOffset = options.Offset + len(commits)
	}
	return page, nil
}

func parseCommits(output []byte) ([]Commit, error) {
	fields := bytes.Split(output, []byte{0})
	commits := make([]Commit, 0, len(fields)/6)
	for offset := 0; offset < len(fields); offset += 6 {
		if len(bytes.TrimSpace(bytes.Join(fields[offset:], nil))) == 0 {
			break
		}
		if offset+6 > len(fields) {
			return nil, errors.New("parse commit log: unexpected Git output")
		}
		commitFields := fields[offset : offset+6]
		commitFields[0] = bytes.Trim(commitFields[0], "\r\n")
		if !isObjectID(string(commitFields[0])) {
			return nil, errors.New("parse commit log: commit ID is invalid")
		}
		authoredAt, err := time.Parse(time.RFC3339, string(commitFields[3]))
		if err != nil {
			return nil, fmt.Errorf("parse commit time: %w", err)
		}
		parents := strings.Fields(string(commitFields[5]))
		if parents == nil {
			parents = []string{}
		}
		for _, parent := range parents {
			if !isObjectID(parent) {
				return nil, errors.New("parse commit log: parent commit ID is invalid")
			}
		}
		commits = append(commits, Commit{
			SHA:         string(commitFields[0]),
			AuthorName:  string(commitFields[1]),
			AuthorEmail: string(commitFields[2]),
			AuthoredAt:  authoredAt,
			Title:       string(commitFields[4]),
			Parents:     parents,
		})
	}
	return commits, nil
}

func resolveRepository(ctx context.Context, repositoryPath string, managedRoots []string) (string, error) {
	repositoryPath, err := allowedExistingPath(repositoryPath, managedRoots)
	if err != nil {
		return "", fmt.Errorf("invalid repository path: %w", err)
	}
	output, err := runGit(ctx, repositoryPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("path is not a Git worktree: %s", cleanGitError(output, err))
	}
	topLevel := strings.TrimSpace(string(output))
	topLevel, err = filepath.Abs(filepath.Clean(topLevel))
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	topLevel, err = filepath.EvalSymlinks(topLevel)
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	requestedInfo, err := os.Stat(repositoryPath)
	if err != nil {
		return "", err
	}
	topLevelInfo, err := os.Stat(topLevel)
	if err != nil {
		return "", err
	}
	if !os.SameFile(requestedInfo, topLevelInfo) {
		return "", errors.New("repository path must be the Git worktree root")
	}
	return repositoryPath, nil
}

func allowedExistingPath(path string, managedRoots []string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path must be a directory")
	}
	if !insideManagedRoot(path, managedRoots) {
		return "", errors.New("path is outside managed roots")
	}
	return path, nil
}

func allowedNewPath(path string, managedRoots []string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		return "", errors.New("repository path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", errors.New("repository parent directory must exist")
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository parent path must be a directory")
	}
	path = filepath.Join(parent, filepath.Base(path))
	if !insideManagedRoot(path, managedRoots) {
		return "", errors.New("path is outside managed roots")
	}
	return path, nil
}

func insideManagedRoot(path string, managedRoots []string) bool {
	for _, root := range managedRoots {
		root = strings.TrimSpace(root)
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		root, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func validateRemoteURL(raw string) error {
	if containsWhitespaceOrControl(raw) {
		return errors.New("remote URL contains invalid whitespace or control characters")
	}
	if validateSCPRemote(raw) {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() {
		return errors.New("remote URL must be an absolute URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if parsed.User != nil {
			return errors.New("HTTPS remote URL must not contain user information")
		}
		if parsed.Host == "" {
			return errors.New("remote URL must include a host")
		}
	case "ssh":
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return errors.New("SSH remote URL must not contain a password")
			}
		}
		if parsed.Host == "" {
			return errors.New("remote URL must include a host")
		}
	default:
		return fmt.Errorf("remote URL scheme %q is not supported", parsed.Scheme)
	}
	return nil
}

func validateSCPRemote(raw string) bool {
	at := strings.LastIndex(raw, "@")
	colon := strings.IndexByte(raw, ':')
	if at <= 0 || colon <= at+1 || colon == len(raw)-1 {
		return false
	}
	user := raw[:at]
	host := raw[at+1 : colon]
	path := raw[colon+1:]
	if strings.ContainsAny(user, "/\\:@") || strings.ContainsAny(host, "/\\:@") || strings.HasPrefix(path, "-") {
		return false
	}
	return user != "" && host != "" && path != ""
}

func validateCommitIdentity(options CreateOptions) error {
	values := []struct {
		name  string
		value string
	}{
		{name: "author name", value: options.AuthorName},
		{name: "author email", value: options.AuthorEmail},
		{name: "commit message", value: options.CommitMessage},
	}
	for _, item := range values {
		if containsControl(item.value) {
			return fmt.Errorf("%s contains control characters", item.name)
		}
	}
	return nil
}

func validateConfiguredCommitIdentity(ctx context.Context, repositoryPath string, options CreateOptions) error {
	checks := []struct {
		name  string
		value string
		key   string
	}{
		{name: "author name", value: options.AuthorName, key: "user.name"},
		{name: "author email", value: options.AuthorEmail, key: "user.email"},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) != "" {
			continue
		}
		output, err := runGit(ctx, repositoryPath, "config", "--get", check.key)
		if err != nil || strings.TrimSpace(string(output)) == "" {
			return fmt.Errorf("%s is not configured; set Git %s or provide it explicitly", check.name, check.key)
		}
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func containsWhitespaceOrControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0
}

func parseSignedCount(value string, prefix byte) (int, error) {
	if len(value) < 2 || value[0] != prefix {
		return 0, fmt.Errorf("invalid divergence count %q", value)
	}
	count, err := strconv.Atoi(value[1:])
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid divergence count %q", value)
	}
	return count, nil
}

func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func splitLines(output []byte) []string {
	raw := strings.Split(strings.TrimSpace(string(output)), "\n")
	result := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func runGit(ctx context.Context, directory string, args ...string) ([]byte, error) {
	commandArgs := args
	if directory != "" {
		commandArgs = append([]string{"-C", directory}, args...)
	}
	return exec.CommandContext(ctx, "git", commandArgs...).CombinedOutput()
}

func cleanGitError(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	return message
}
