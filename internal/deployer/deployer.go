package deployer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/prerequisite"
	"github.com/wio-platform/wio/internal/protocol"
)

type StatusFunc func(status, message, resolvedCommit, content string)

const maximumProcessLogSize = 1 << 20

type Deployer struct {
	Docker             string
	PrerequisiteSocket string
	mu                 sync.Mutex
}

type PreflightCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func New(dockerPath string, prerequisiteSocket ...string) *Deployer {
	if dockerPath == "" {
		dockerPath = "docker"
	}
	socket := prerequisite.DefaultSocket
	if len(prerequisiteSocket) > 0 && prerequisiteSocket[0] != "" {
		socket = prerequisiteSocket[0]
	}
	return &Deployer{Docker: dockerPath, PrerequisiteSocket: socket}
}

func (d *Deployer) Deploy(ctx context.Context, command protocol.DeployCommand, status StatusFunc) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if runtime.GOOS != "linux" {
		return errors.New("Compose deployments are supported only on Linux agents")
	}
	checks := d.Preflight(ctx, command)
	if missingAutomaticPrerequisite(checks) {
		status("preparing", "installing missing deployment prerequisites", "", "Missing Git, Docker, Docker Compose, or the Docker daemon will be configured automatically when supported.")
		result, err := prerequisite.Ensure(ctx, d.PrerequisiteSocket)
		for _, log := range result.Logs {
			status("preparing", "deployment prerequisite setup", "", log)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, net.ErrClosed) {
				err = fmt.Errorf("%w; this Agent predates automatic prerequisite setup, so re-register the server once to install its restricted helper", err)
			}
			status("failed", "deployment prerequisite setup failed", "", err.Error())
			return fmt.Errorf("install deployment prerequisites: %w", err)
		}
		checks = d.Preflight(ctx, command)
	}
	for _, check := range checks {
		state := "preparing"
		if !check.OK {
			state = "failed"
		}
		status(state, "environment check: "+check.Name, "", check.Message)
		if !check.OK {
			return fmt.Errorf("deployment environment check failed: %s: %s", check.Name, check.Message)
		}
	}
	root, release, err := releasePath(command.ReleaseRoot, command.TargetID, command.DeploymentID)
	if err != nil {
		return err
	}
	status("preparing", "creating release workspace", "", "Preparing an isolated release directory.")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(release); err == nil {
		return errors.New("release already exists")
	}
	source := command.Repository
	cloneArgs := []string{"clone", "--no-checkout", "--", source, release}
	checkoutRef := "FETCH_HEAD"
	if command.SourceType == "workspace" {
		source = command.SourcePath
		cloneArgs = []string{"clone", "--local", "--no-hardlinks", "--no-checkout", "--", source, release}
		checkoutRef = command.CommitRef
		if checkoutRef == "" {
			checkoutRef = "HEAD"
		}
		resolvedSource, resolveErr := run(ctx, nil, "git", "-C", source, "rev-parse", "--verify", checkoutRef+"^{commit}")
		if resolveErr != nil {
			status("preparing", "workspace revision resolution failed", "", resolvedSource)
			return fmt.Errorf("resolve workspace revision: %w: %s", resolveErr, resolvedSource)
		}
		checkoutRef = strings.TrimSpace(resolvedSource)
	}
	if output, err := run(ctx, nil, "git", cloneArgs...); err != nil {
		status("preparing", "repository clone failed", "", output)
		return fmt.Errorf("clone repository: %w: %s", err, output)
	} else {
		status("preparing", "repository cloned", "", output)
	}
	if command.SourceType == "workspace" {
		status("preparing", "using server workspace", checkoutRef, source)
	} else if output, err := run(ctx, nil, "git", "-C", release, "fetch", "--depth=1", "origin", command.CommitRef); err != nil {
		status("preparing", "commit fetch failed", "", output)
		return fmt.Errorf("fetch commit: %w: %s", err, output)
	} else {
		status("preparing", "commit fetched", "", output)
	}
	if output, err := run(ctx, nil, "git", "-C", release, "checkout", "--detach", checkoutRef); err != nil {
		status("preparing", "commit checkout failed", "", output)
		return fmt.Errorf("checkout commit: %w: %s", err, output)
	} else {
		status("preparing", "commit checked out", "", output)
	}
	resolved, err := run(ctx, nil, "git", "-C", release, "rev-parse", "HEAD")
	if err != nil {
		status("preparing", "commit resolution failed", "", resolved)
		return fmt.Errorf("resolve commit: %w", err)
	}
	resolved = strings.TrimSpace(resolved)
	workDir, composePath, err := composePaths(release, command.WorkingDir, command.ComposeFile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(composePath); err != nil {
		return fmt.Errorf("compose file: %w", err)
	}
	environment := make([]string, 0, len(command.Environment))
	for key, value := range command.Environment {
		if strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("invalid environment key %q", key)
		}
		environment = append(environment, key+"="+value)
	}
	project := projectName(command.TargetID)
	status("running", "starting Docker Compose project", resolved, "Compose project initialization started.")
	if command.BuildMode == "pull" {
		if output, err := d.compose(ctx, workDir, environment, project, composePath, "pull"); err != nil {
			status("running", "image pull failed", resolved, output)
			return fmt.Errorf("docker compose pull: %w: %s", err, output)
		} else {
			status("running", "images pulled", resolved, output)
		}
	}
	args := []string{"up", "-d", "--remove-orphans"}
	if command.BuildMode != "pull" {
		args = append(args, "--build")
	}
	if output, err := d.compose(ctx, workDir, environment, project, composePath, args...); err != nil {
		status("running", "Docker Compose start failed", resolved, output)
		return fmt.Errorf("docker compose up: %w: %s", err, output)
	} else {
		status("running", "Docker Compose project started", resolved, output)
	}
	for _, check := range command.HealthChecks {
		status("running", "running health check", resolved, check.Address)
		if err := healthCheck(ctx, check); err != nil {
			return err
		}
	}
	status("running", "health checks passed", resolved, "All configured health checks passed.")
	if err := promote(root, release); err != nil {
		return err
	}
	status("succeeded", "deployment is healthy", resolved, "Release promoted and marked as current.")
	return nil
}

func missingAutomaticPrerequisite(checks []PreflightCheck) bool {
	for _, check := range checks {
		if !check.OK && (check.Name == "Git" || check.Name == "Docker daemon" || check.Name == "Docker Compose") {
			return true
		}
	}
	return false
}

func (d *Deployer) Preflight(ctx context.Context, command protocol.DeployCommand) []PreflightCheck {
	checks := make([]PreflightCheck, 0, 6)
	addCommand := func(name, executable string, args ...string) {
		output, err := run(ctx, nil, executable, args...)
		message := strings.TrimSpace(output)
		if err != nil {
			if message == "" {
				message = err.Error()
			}
			checks = append(checks, PreflightCheck{Name: name, OK: false, Message: message})
			return
		}
		if message == "" {
			message = "available"
		}
		checks = append(checks, PreflightCheck{Name: name, OK: true, Message: message})
	}
	if runtime.GOOS != "linux" {
		return []PreflightCheck{{Name: "Linux", OK: false, Message: "Compose deployments require a Linux Agent"}}
	}
	addCommand("Git", "git", "--version")
	addCommand("Docker daemon", d.Docker, "info", "--format", "{{.ServerVersion}}")
	addCommand("Docker Compose", d.Docker, "compose", "version", "--short")
	if command.ReleaseRoot == "" || !filepath.IsAbs(command.ReleaseRoot) {
		checks = append(checks, PreflightCheck{Name: "release directory", OK: false, Message: "release root must be an absolute path"})
	} else if err := writableDirectory(command.ReleaseRoot); err != nil {
		checks = append(checks, PreflightCheck{Name: "release directory", OK: false, Message: err.Error()})
	} else {
		checks = append(checks, PreflightCheck{Name: "release directory", OK: true, Message: command.ReleaseRoot + " is writable"})
	}
	if command.SourceType == "workspace" {
		if info, err := os.Stat(command.SourcePath); err != nil || !info.IsDir() {
			message := "workspace directory is unavailable"
			if err != nil {
				message = err.Error()
			}
			checks = append(checks, PreflightCheck{Name: "project workspace", OK: false, Message: message})
		} else if output, err := run(ctx, nil, "git", "-C", command.SourcePath, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(output) != "true" {
			checks = append(checks, PreflightCheck{Name: "project workspace", OK: false, Message: "selected directory is not a Git worktree"})
		} else {
			checks = append(checks, PreflightCheck{Name: "project workspace", OK: true, Message: command.SourcePath})
		}
		_, composePath, err := composePaths(command.SourcePath, command.WorkingDir, command.ComposeFile)
		if err != nil {
			checks = append(checks, PreflightCheck{Name: "Compose file", OK: false, Message: err.Error()})
		} else if info, statErr := os.Stat(composePath); statErr != nil || info.IsDir() {
			message := "Compose file is unavailable"
			if statErr != nil {
				message = statErr.Error()
			}
			checks = append(checks, PreflightCheck{Name: "Compose file", OK: false, Message: message})
		} else {
			checks = append(checks, PreflightCheck{Name: "Compose file", OK: true, Message: composePath})
		}
	}
	return checks
}

func writableDirectory(path string) error {
	clean := filepath.Clean(path)
	ancestor := clean
	for {
		info, err := os.Stat(ancestor)
		if err == nil {
			if !info.IsDir() {
				return errors.New("release path is not a directory")
			}
			probe, err := os.CreateTemp(ancestor, ".wio-preflight-*")
			if err != nil {
				return fmt.Errorf("release directory is not writable: %w", err)
			}
			name := probe.Name()
			_ = probe.Close()
			_ = os.Remove(name)
			return nil
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return fmt.Errorf("release directory is unavailable: %w", err)
		}
		ancestor = parent
	}
}

func (d *Deployer) Rollback(ctx context.Context, command protocol.RollbackCommand, status StatusFunc) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if runtime.GOOS != "linux" {
		return errors.New("Compose rollbacks are supported only on Linux agents")
	}
	root := filepath.Join(filepath.Clean(command.ReleaseRoot), command.TargetID)
	previousLink := filepath.Join(root, "previous")
	previous, err := filepath.EvalSymlinks(previousLink)
	if err != nil {
		return errors.New("no previous release is available")
	}
	if !within(root, previous) {
		return errors.New("previous release points outside the target release root")
	}
	workDir, composePath, err := composePaths(previous, command.WorkingDir, command.ComposeFile)
	if err != nil {
		return err
	}
	status("running", "restoring previous Compose release", "", "Starting Docker Compose from the previous release.")
	if output, err := d.compose(ctx, workDir, nil, projectName(command.TargetID), composePath, "up", "-d", "--remove-orphans"); err != nil {
		status("running", "rollback Compose start failed", "", output)
		return fmt.Errorf("docker compose rollback: %w: %s", err, output)
	} else {
		status("running", "previous Compose release started", "", output)
	}
	currentLink := filepath.Join(root, "current")
	current, _ := filepath.EvalSymlinks(currentLink)
	if err := replaceSymlink(currentLink, previous); err != nil {
		return err
	}
	if current != "" && within(root, current) {
		_ = replaceSymlink(previousLink, current)
	}
	resolved, _ := run(ctx, nil, "git", "-C", previous, "rev-parse", "HEAD")
	status("rolled_back", "previous release restored", strings.TrimSpace(resolved), "Previous release promoted and marked as current.")
	return nil
}

// ContainerAction applies a narrowly scoped Compose lifecycle command to the
// target's current release. It deliberately never accepts arbitrary command
// arguments and never passes --volumes, so removing containers preserves named
// volumes and deployment history.
func (d *Deployer) ContainerAction(ctx context.Context, command protocol.ContainerActionCommand) (protocol.ContainerActionResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := protocol.ContainerActionResult{TargetID: command.TargetID, Action: command.Action}
	if runtime.GOOS != "linux" {
		return result, errors.New("Compose container actions are supported only on Linux agents")
	}
	_, current, err := currentRelease(command.ReleaseRoot, command.TargetID)
	if err != nil {
		return result, err
	}
	workDir, composePath, err := composePaths(current, command.WorkingDir, command.ComposeFile)
	if err != nil {
		return result, err
	}
	if info, statErr := os.Stat(composePath); statErr != nil || info.IsDir() {
		if statErr != nil {
			return result, fmt.Errorf("compose file: %w", statErr)
		}
		return result, errors.New("compose file is a directory")
	}
	environment := make([]string, 0, len(command.Environment))
	for key, value := range command.Environment {
		if strings.ContainsAny(key, "=\x00") {
			return result, fmt.Errorf("invalid environment key %q", key)
		}
		environment = append(environment, key+"="+value)
	}
	args, state, message, err := containerActionSpec(command.Action)
	if err != nil {
		return result, err
	}
	result.State = state
	output, err := d.compose(ctx, workDir, environment, projectName(command.TargetID), composePath, args...)
	result.Content = truncate(output, maximumProcessLogSize)
	if err != nil {
		result.State = "failed"
		result.Message = fmt.Sprintf("Docker Compose %s failed", command.Action)
		return result, fmt.Errorf("docker compose %s: %w: %s", command.Action, err, output)
	}
	result.Message = message
	return result, nil
}

func containerActionSpec(action string) ([]string, string, string, error) {
	switch action {
	case "start":
		// `up` also recreates a container after a previous remove operation.
		return []string{"up", "-d", "--no-build", "--remove-orphans"}, "running", "Docker Compose project started", nil
	case "stop":
		return []string{"stop"}, "stopped", "Docker Compose project stopped", nil
	case "restart":
		return []string{"restart"}, "running", "Docker Compose project restarted", nil
	case "remove":
		return []string{"down", "--remove-orphans"}, "removed", "Docker Compose project removed", nil
	default:
		return nil, "", "", fmt.Errorf("unsupported container action %q", action)
	}
}

func currentRelease(releaseRoot, targetID string) (string, string, error) {
	if releaseRoot == "" || !filepath.IsAbs(releaseRoot) {
		return "", "", errors.New("release root must be absolute")
	}
	if !safeID(targetID) {
		return "", "", errors.New("invalid deployment target identifier")
	}
	root := filepath.Join(filepath.Clean(releaseRoot), targetID)
	link := filepath.Join(root, "current")
	current, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", "", errors.New("no current release is available")
	}
	if !within(root, current) {
		return "", "", errors.New("current release points outside the target release root")
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", "", fmt.Errorf("current release: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("current release is not a directory")
	}
	return root, current, nil
}

func (d *Deployer) compose(ctx context.Context, directory string, environment []string, project, composeFile string, args ...string) (string, error) {
	base := []string{"compose", "--project-name", project, "--file", composeFile}
	return runIn(ctx, directory, environment, d.Docker, append(base, args...)...)
}

func releasePath(releaseRoot, targetID, deploymentID string) (string, string, error) {
	if releaseRoot == "" || !filepath.IsAbs(releaseRoot) {
		return "", "", errors.New("release root must be absolute")
	}
	if !safeID(targetID) || !safeID(deploymentID) {
		return "", "", errors.New("invalid deployment identifier")
	}
	root := filepath.Join(filepath.Clean(releaseRoot), targetID)
	release := filepath.Join(root, "releases", deploymentID)
	return root, release, nil
}

func composePaths(release, workingDir, composeFile string) (string, string, error) {
	workDir := filepath.Join(release, filepath.Clean(workingDir))
	if workingDir == "" {
		workDir = release
	}
	if !within(release, workDir) {
		return "", "", errors.New("working directory escapes the release")
	}
	if composeFile == "" {
		composeFile = "compose.yaml"
	}
	composePath := filepath.Join(workDir, filepath.Clean(composeFile))
	if !within(workDir, composePath) {
		return "", "", errors.New("compose file escapes the working directory")
	}
	return workDir, composePath, nil
}

func promote(root, release string) error {
	currentLink := filepath.Join(root, "current")
	previousLink := filepath.Join(root, "previous")
	current, _ := filepath.EvalSymlinks(currentLink)
	if current != "" && within(root, current) {
		if err := replaceSymlink(previousLink, current); err != nil {
			return err
		}
	}
	return replaceSymlink(currentLink, release)
}

func replaceSymlink(link, target string) error {
	temporary := link + ".new"
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	_ = os.Remove(link)
	return os.Rename(temporary, link)
}

func healthCheck(ctx context.Context, check protocol.HealthCheck) error {
	timeout := time.Duration(check.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		var err error
		switch check.Type {
		case "http", "https":
			client := &http.Client{Timeout: 5 * time.Second}
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, check.Address, nil)
			if requestErr != nil {
				return requestErr
			}
			response, requestErr := client.Do(request)
			err = requestErr
			if response != nil {
				response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 400 {
					return nil
				}
				err = fmt.Errorf("HTTP status %d", response.StatusCode)
			}
		case "tcp":
			connection, dialErr := net.DialTimeout("tcp", check.Address, 5*time.Second)
			err = dialErr
			if dialErr == nil {
				connection.Close()
				return nil
			}
		default:
			return fmt.Errorf("unsupported health check type %q", check.Type)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("health check %s failed: %w", check.Address, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func run(ctx context.Context, environment []string, command string, args ...string) (string, error) {
	return runIn(ctx, "", environment, command, args...)
}

func runIn(ctx context.Context, directory string, environment []string, command string, args ...string) (string, error) {
	process := exec.CommandContext(ctx, command, args...)
	process.Dir = directory
	process.Env = append(os.Environ(), environment...)
	output, err := process.CombinedOutput()
	return truncate(string(output), maximumProcessLogSize), err
}

func within(root, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func projectName(targetID string) string {
	clean := strings.ReplaceAll(targetID, "_", "-")
	if len(clean) > 24 {
		clean = clean[:24]
	}
	return "wio-" + strings.ToLower(clean)
}

func truncate(value string, size int) string {
	value = strings.TrimSpace(value)
	if len(value) <= size {
		return value
	}
	return value[:size] + "..."
}
