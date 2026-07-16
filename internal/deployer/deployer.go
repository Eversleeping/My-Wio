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

	"github.com/wio-platform/wio/internal/protocol"
)

type StatusFunc func(status, message, resolvedCommit string)

type Deployer struct {
	Docker string
	mu     sync.Mutex
}

func New(dockerPath string) *Deployer {
	if dockerPath == "" {
		dockerPath = "docker"
	}
	return &Deployer{Docker: dockerPath}
}

func (d *Deployer) Deploy(ctx context.Context, command protocol.DeployCommand, status StatusFunc) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if runtime.GOOS != "linux" {
		return errors.New("Compose deployments are supported only on Linux agents")
	}
	root, release, err := releasePath(command.ReleaseRoot, command.TargetID, command.DeploymentID)
	if err != nil {
		return err
	}
	status("preparing", "creating release workspace", "")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(release); err == nil {
		return errors.New("release already exists")
	}
	if output, err := run(ctx, nil, "git", "clone", "--no-checkout", "--", command.Repository, release); err != nil {
		return fmt.Errorf("clone repository: %w: %s", err, output)
	}
	if output, err := run(ctx, nil, "git", "-C", release, "fetch", "--depth=1", "origin", command.CommitRef); err != nil {
		return fmt.Errorf("fetch commit: %w: %s", err, output)
	}
	if output, err := run(ctx, nil, "git", "-C", release, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout commit: %w: %s", err, output)
	}
	resolved, err := run(ctx, nil, "git", "-C", release, "rev-parse", "HEAD")
	if err != nil {
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
	status("running", "starting Docker Compose project", resolved)
	if command.BuildMode == "pull" {
		if output, err := d.compose(ctx, workDir, environment, project, composePath, "pull"); err != nil {
			return fmt.Errorf("docker compose pull: %w: %s", err, output)
		}
	}
	args := []string{"up", "-d", "--remove-orphans"}
	if command.BuildMode != "pull" {
		args = append(args, "--build")
	}
	if output, err := d.compose(ctx, workDir, environment, project, composePath, args...); err != nil {
		return fmt.Errorf("docker compose up: %w: %s", err, output)
	}
	for _, check := range command.HealthChecks {
		if err := healthCheck(ctx, check); err != nil {
			return err
		}
	}
	if err := promote(root, release); err != nil {
		return err
	}
	status("succeeded", "deployment is healthy", resolved)
	return nil
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
	status("running", "restoring previous Compose release", "")
	if output, err := d.compose(ctx, workDir, nil, projectName(command.TargetID), composePath, "up", "-d", "--remove-orphans"); err != nil {
		return fmt.Errorf("docker compose rollback: %w: %s", err, output)
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
	status("rolled_back", "previous release restored", strings.TrimSpace(resolved))
	return nil
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
	return truncate(string(output), 64<<10), err
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
