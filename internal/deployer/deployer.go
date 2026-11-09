package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/deploymenttemplate"
	"github.com/wio-platform/wio/internal/prerequisite"
	"github.com/wio-platform/wio/internal/protocol"
)

type StatusFunc func(status, message, resolvedCommit, detectedPublicURL, content string)

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
	ref, err := deploymentRef(command)
	if err != nil {
		return err
	}
	checks := d.Preflight(ctx, command)
	if missingAutomaticPrerequisite(checks) {
		status("preparing", "installing missing deployment prerequisites", "", "", "Missing Git, Docker, Docker Compose, or the Docker daemon will be configured automatically when supported.")
		result, err := prerequisite.Ensure(ctx, d.PrerequisiteSocket)
		for _, log := range result.Logs {
			status("preparing", "deployment prerequisite setup", "", "", log)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, net.ErrClosed) {
				err = fmt.Errorf("%w; this Agent predates automatic prerequisite setup, so re-register the server once to install its restricted helper", err)
			}
			status("failed", "deployment prerequisite setup failed", "", "", err.Error())
			return fmt.Errorf("install deployment prerequisites: %w", err)
		}
		checks = d.Preflight(ctx, command)
	}
	for _, check := range checks {
		state := "preparing"
		if !check.OK {
			state = "failed"
		}
		status(state, "environment check: "+check.Name, "", "", check.Message)
		if !check.OK {
			return fmt.Errorf("deployment environment check failed: %s: %s", check.Name, check.Message)
		}
	}
	root, release, err := releasePath(command.ReleaseRoot, command.TargetID, command.DeploymentID)
	if err != nil {
		return err
	}
	status("preparing", "creating release workspace", "", "", "Preparing an isolated release directory.")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	if _, err := os.Lstat(release); err == nil {
		return errors.New("release already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The release running now is what a failed deployment has to bring back.
	previous := ""
	if _, current, err := currentRelease(command.ReleaseRoot, command.TargetID); err == nil {
		previous = current
	}
	if previous != "" {
		if recovery := command.Recovery; recovery != nil {
			_, expected, err := releasePath(command.ReleaseRoot, command.TargetID, recovery.ReleaseDeploymentID)
			if err == nil && filepath.Clean(expected) != filepath.Clean(previous) {
				err = errors.New("historical recovery configuration does not match the current release")
			}
			if err == nil {
				_, _, err = composePaths(previous, recovery.WorkingDir, recovery.ComposeFile)
			}
			if err == nil {
				_, err = composeEnvironment(recovery.Environment, recovery.PublicURL)
			}
			if err != nil {
				// The controller's last successful deployment can lag the Agent's
				// current release. Do not let stale recovery metadata block a deploy.
				command.Recovery = nil
				status("preparing", "historical recovery configuration ignored", "", "", "Warning: "+err.Error()+"; recovery will use the incoming configuration as a best effort.")
			}
		} else {
			status("preparing", "historical recovery configuration unavailable", "", "", "The controller or previous release has no recovery snapshot; recovery will use the incoming configuration as a best effort.")
		}
	}
	progress := &releaseProgress{}
	if err := d.deployRelease(ctx, command, ref, status, root, release, progress); err != nil {
		return d.abandonRelease(ctx, command, status, root, release, previous, progress, err)
	}
	return nil
}

// releaseProgress records how far a deployment got, for cleanup after a
// failure.
type releaseProgress struct {
	workDir     string
	composePath string
	environment []string
	// started is set once Docker Compose may have replaced the target's
	// running containers with the new release.
	started bool
}

func (d *Deployer) deployRelease(ctx context.Context, command protocol.DeployCommand, ref string, status StatusFunc, root, release string, progress *releaseProgress) error {
	source := command.Repository
	cloneArgs := []string{"clone", "--no-checkout", "--", source, release}
	checkoutRef := "FETCH_HEAD"
	if command.SourceType == "workspace" {
		source = command.SourcePath
		cloneArgs = []string{"clone", "--local", "--no-hardlinks", "--no-checkout", "--", source, release}
		resolvedSource, resolveErr := run(ctx, nil, "git", "-C", source, "rev-parse", "--verify", ref+"^{commit}")
		if resolveErr != nil {
			status("preparing", "workspace revision resolution failed", "", "", resolvedSource)
			return fmt.Errorf("resolve workspace revision: %w: %s", resolveErr, resolvedSource)
		}
		checkoutRef = strings.TrimSpace(resolvedSource)
		if !objectIDPattern.MatchString(checkoutRef) {
			return fmt.Errorf("resolve workspace revision: unexpected Git output %q", checkoutRef)
		}
	}
	if output, err := run(ctx, nil, "git", cloneArgs...); err != nil {
		status("preparing", "repository clone failed", "", "", output)
		return fmt.Errorf("clone repository: %w: %s", err, output)
	} else {
		status("preparing", "repository cloned", "", "", output)
	}
	if command.SourceType == "workspace" {
		status("preparing", "using server workspace", checkoutRef, "", source)
	} else if output, err := run(ctx, nil, "git", "-C", release, "fetch", "--depth=1", "--", "origin", ref); err != nil {
		status("preparing", "commit fetch failed", "", "", output)
		return fmt.Errorf("fetch commit: %w: %s", err, output)
	} else {
		status("preparing", "commit fetched", "", "", output)
	}
	if output, err := run(ctx, nil, "git", "-C", release, "checkout", "--detach", checkoutRef); err != nil {
		status("preparing", "commit checkout failed", "", "", output)
		return fmt.Errorf("checkout commit: %w: %s", err, output)
	} else {
		status("preparing", "commit checked out", "", "", output)
	}
	resolved, err := run(ctx, nil, "git", "-C", release, "rev-parse", "HEAD")
	if err != nil {
		status("preparing", "commit resolution failed", "", "", resolved)
		return fmt.Errorf("resolve commit: %w", err)
	}
	resolved = strings.TrimSpace(resolved)
	workDir, composePath, err := composePaths(release, command.WorkingDir, command.ComposeFile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(composePath); err != nil {
		if !allowsTemplateGeneration(command) {
			return fmt.Errorf("compose file: %w", err)
		}
		generated, generateErr := deploymenttemplate.Generate(deploymenttemplate.GenerateOptions{
			Root:        workDir,
			ComposeFile: command.ComposeFile,
			TemplateID:  command.TemplateID,
			BuildMode:   command.BuildMode,
			PublicPort:  publicPort(command.PublicURL),
		})
		if generateErr != nil {
			status("preparing", "deployment template generation failed", resolved, "", generateErr.Error())
			return fmt.Errorf("generate deployment files: %w", generateErr)
		}
		composePath = generated.ComposePath
		status("preparing", "deployment files generated", resolved, "", fmt.Sprintf("Template %s v%s generated: %s", generated.Template.ID, generated.Template.Version, strings.Join(generated.Files, ", ")))
	}
	environment, err := composeEnvironment(command.Environment, command.PublicURL)
	if err != nil {
		return err
	}
	progress.workDir, progress.composePath, progress.environment = workDir, composePath, environment
	project := projectName(command.TargetID)
	status("running", "starting Docker Compose project", resolved, "", "Compose project initialization started.")
	args := []string{"up", "-d", "--remove-orphans"}
	if command.BuildMode == "pull" {
		if output, err := d.compose(ctx, workDir, environment, project, composePath, "pull"); err != nil {
			status("running", "image pull failed", resolved, "", output)
			return fmt.Errorf("docker compose pull: %w: %s", err, output)
		} else {
			status("running", "images pulled", resolved, "", output)
		}
	} else {
		// Build before `up` so a failing build leaves the running release
		// untouched.
		if output, err := d.compose(ctx, workDir, environment, project, composePath, "build"); err != nil {
			status("running", "image build failed", resolved, "", output)
			return fmt.Errorf("docker compose build: %w: %s", err, output)
		} else {
			status("running", "images built", resolved, "", output)
		}
		args = append(args, "--no-build")
	}
	// `up` replaces the containers of the previous release (same project), so
	// every failure from here on has to restore it.
	progress.started = true
	if output, err := d.compose(ctx, workDir, environment, project, composePath, args...); err != nil {
		status("running", "Docker Compose start failed", resolved, "", output)
		return fmt.Errorf("docker compose up: %w: %s", err, output)
	} else {
		status("running", "Docker Compose project started", resolved, "", output)
	}
	detectedPublicURL := ""
	if output, inspectErr := d.compose(ctx, workDir, environment, project, composePath, "ps", "--format", "json"); inspectErr == nil {
		if strings.TrimSpace(command.PublicURL) == "" {
			detectedPublicURL = composePublicURL(command.ServerAddress, output)
		} else if port := publicPort(command.PublicURL); port > 0 && !composePublishesPort(output, port) {
			return fmt.Errorf("configured public port %d is not published by Docker Compose", port)
		}
	} else if publicPort(command.PublicURL) > 0 {
		return fmt.Errorf("inspect Docker Compose published ports: %w: %s", inspectErr, output)
	}
	for _, check := range command.HealthChecks {
		status("running", "running health check", resolved, "", check.Address)
		if err := healthCheck(ctx, check); err != nil {
			return err
		}
	}
	status("running", "health checks passed", resolved, "", "All configured health checks passed.")
	if err := d.saveReleaseImages(ctx, release, workDir, environment, project, composePath); err != nil {
		return fmt.Errorf("save release images: %w", err)
	}
	if err := promote(root, release); err != nil {
		return err
	}
	status("succeeded", "deployment is healthy", resolved, detectedPublicURL, "Release promoted and marked as current.")
	return nil
}

// releaseCleanupTimeout bounds the cleanup after a failed deployment. Cleanup
// runs even when the deployment's own context was cancelled.
const releaseCleanupTimeout = 10 * time.Minute

// abandonRelease cleans up after a deployment that failed once its release
// directory existed. If Docker Compose may already have replaced the running
// containers, the previous release is started again exactly like a rollback
// (or the failed project is stopped when there is no previous release). The
// failed release's files and retained image tags are removed either way, and
// the returned error says what happened to the running project.
func (d *Deployer) abandonRelease(ctx context.Context, command protocol.DeployCommand, status StatusFunc, root, release, previous string, progress *releaseProgress, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseCleanupTimeout)
	defer cancel()
	var outcomes []string
	if progress.started {
		outcomes = append(outcomes, d.recoverProject(cleanupCtx, command, status, root, release, previous, progress))
	}
	if failures := d.removeReleaseImageTags(cleanupCtx, release); len(failures) > 0 {
		outcomes = append(outcomes, "some image tags of the failed release could not be removed: "+strings.Join(failures, "; "))
	}
	if err := os.RemoveAll(release); err != nil {
		outcomes = append(outcomes, "the failed release files could not be removed: "+err.Error())
	}
	if len(outcomes) == 0 {
		return cause
	}
	return fmt.Errorf("%w; %s", cause, strings.Join(outcomes, "; "))
}

// recoverProject brings the target back to its previous release after a
// failed `up`, or stops the failed release when there is nothing to restore.
// It returns a description of the outcome for the deployment error.
func (d *Deployer) recoverProject(ctx context.Context, command protocol.DeployCommand, status StatusFunc, root, release, previous string, progress *releaseProgress) string {
	project := projectName(command.TargetID)
	if previous != "" {
		status("running", "restoring previous release", "", "", "The deployment failed after Docker Compose started it; starting the previous release again.")
		workingDir, composeFile, environment := command.WorkingDir, command.ComposeFile, progress.environment
		if recovery := command.Recovery; recovery != nil {
			workingDir, composeFile = recovery.WorkingDir, recovery.ComposeFile
			var err error
			environment, err = composeEnvironment(recovery.Environment, recovery.PublicURL)
			if err != nil {
				return "restoring the previous release failed: " + err.Error()
			}
		}
		output, err := d.restoreRelease(ctx, previous, workingDir, composeFile, environment, project)
		if err != nil {
			if output == "" {
				output = err.Error()
			}
			status("running", "previous release restore failed", "", "", output)
			return fmt.Sprintf("restoring the previous release failed, so the failed release may still be running: %v", err)
		}
		status("running", "previous release restored", "", "", output)
		// A failed promotion can leave the current link missing.
		currentLink := filepath.Join(root, "current")
		if current, err := filepath.EvalSymlinks(currentLink); err != nil || filepath.Clean(current) != filepath.Clean(previous) {
			if err := replaceSymlink(currentLink, previous); err != nil {
				return "the previous release was restored but could not be marked as current: " + err.Error()
			}
		}
		return "the previous release was restored"
	}
	status("running", "stopping failed release", "", "", "The deployment failed and there is no previous release to restore; stopping the failed release.")
	output, err := d.compose(ctx, progress.workDir, progress.environment, project, progress.composePath, "down", "--remove-orphans")
	if err != nil {
		if output == "" {
			output = err.Error()
		}
		status("running", "failed release could not be stopped", "", "", output)
		return fmt.Sprintf("stopping the failed release failed, so it may still be running: %v: %s", err, output)
	}
	status("running", "failed release stopped", "", "", output)
	return "there was no previous release to restore, so the failed release was stopped"
}

// restoreRelease starts a historical release from its pinned image snapshot,
// exactly as a rollback does.
func (d *Deployer) restoreRelease(ctx context.Context, release, workingDir, composeFile string, environment []string, project string) (string, error) {
	workDir, composePath, err := composePaths(release, workingDir, composeFile)
	if err != nil {
		return "", err
	}
	return d.pinnedCompose(ctx, release, workDir, environment, project, composePath, "up", "-d", "--remove-orphans")
}

func missingAutomaticPrerequisite(checks []PreflightCheck) bool {
	for _, check := range checks {
		if !check.OK && (check.Name == "Git" || check.Name == "Docker daemon" || check.Name == "Docker Compose") {
			return true
		}
	}
	return false
}

func allowsTemplateGeneration(command protocol.DeployCommand) bool {
	return strings.EqualFold(strings.TrimSpace(command.DeploymentMode), deploymenttemplate.ModeAuto) || strings.EqualFold(strings.TrimSpace(command.DeploymentMode), "template")
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
		workDir, composePath, err := composePaths(command.SourcePath, command.WorkingDir, command.ComposeFile)
		if err != nil {
			checks = append(checks, PreflightCheck{Name: "Compose file", OK: false, Message: err.Error()})
		} else if info, statErr := os.Stat(composePath); statErr != nil || info.IsDir() {
			if allowsTemplateGeneration(command) {
				if template, templateErr := deploymenttemplate.Detect(workDir); templateErr == nil {
					checks = append(checks, PreflightCheck{Name: "Compose template", OK: true, Message: fmt.Sprintf("%s v%s will generate deployment files", template.ID, template.Version)})
				} else {
					message := "Compose file is unavailable"
					if statErr != nil {
						message = statErr.Error() + "; " + templateErr.Error()
					}
					checks = append(checks, PreflightCheck{Name: "Compose file", OK: false, Message: message})
				}
			} else {
				message := "Compose file is unavailable"
				if statErr != nil {
					message = statErr.Error()
				}
				checks = append(checks, PreflightCheck{Name: "Compose file", OK: false, Message: message})
			}
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
	root, release, err := releasePath(command.ReleaseRoot, command.TargetID, command.ReleaseDeploymentID)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(release); statErr != nil || !info.IsDir() {
		if statErr != nil {
			return fmt.Errorf("rollback release is unavailable: %w", statErr)
		}
		return errors.New("rollback release is not a directory")
	}
	currentLink := filepath.Join(root, "current")
	current, currentErr := filepath.EvalSymlinks(currentLink)
	if currentErr == nil && filepath.Clean(current) == filepath.Clean(release) {
		return errors.New("rollback release is already current")
	}
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return fmt.Errorf("resolve current release: %w", currentErr)
	}
	if current != "" && !within(root, current) {
		return errors.New("current release points outside the target release root")
	}
	if _, _, err := composePaths(release, command.WorkingDir, command.ComposeFile); err != nil {
		return err
	}
	environment, err := composeEnvironment(command.Environment, command.PublicURL)
	if err != nil {
		return err
	}
	status("running", "restoring selected Compose release", "", "", "Starting Docker Compose from the selected historical release.")
	if output, err := d.restoreRelease(ctx, release, command.WorkingDir, command.ComposeFile, environment, projectName(command.TargetID)); err != nil {
		status("running", "rollback Compose start failed", "", "", output)
		return fmt.Errorf("docker compose rollback: %w: %s", err, output)
	} else {
		status("running", "selected Compose release started", "", "", output)
	}
	previousLink := filepath.Join(root, "previous")
	if current != "" {
		if err := replaceSymlink(previousLink, current); err != nil {
			return err
		}
	}
	if err := replaceSymlink(currentLink, release); err != nil {
		return err
	}
	resolved, _ := run(ctx, nil, "git", "-C", release, "rev-parse", "HEAD")
	status("rolled_back", "selected release restored", strings.TrimSpace(resolved), "", "Selected historical release promoted and marked as current.")
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
	if command.Action == "delete" {
		return d.deleteTarget(ctx, command)
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
	environment, err := composeEnvironment(command.Environment, command.PublicURL)
	if err != nil {
		return result, err
	}
	args, state, message, err := containerActionSpec(command.Action)
	if err != nil {
		return result, err
	}
	result.State = state
	var output string
	if _, statErr := os.Stat(filepath.Join(current, releaseImagesFile)); statErr == nil {
		output, err = d.pinnedCompose(ctx, current, workDir, environment, projectName(command.TargetID), composePath, args...)
	} else if errors.Is(statErr, os.ErrNotExist) {
		// Existing releases can still be stopped/removed; only exact rollback
		// requires a snapshot recorded by the new deployment flow.
		output, err = d.compose(ctx, workDir, environment, projectName(command.TargetID), composePath, args...)
	} else {
		err = statErr
	}
	result.Content = truncate(output, maximumProcessLogSize)
	if err != nil {
		result.State = "failed"
		result.Message = fmt.Sprintf("Docker Compose %s failed", command.Action)
		return result, fmt.Errorf("docker compose %s: %w: %s", command.Action, err, output)
	}
	result.Message = message
	return result, nil
}

func (d *Deployer) deleteTarget(ctx context.Context, command protocol.ContainerActionCommand) (protocol.ContainerActionResult, error) {
	result := protocol.ContainerActionResult{TargetID: command.TargetID, Action: command.Action, State: "removed"}
	root, err := deploymentTargetRoot(command.ReleaseRoot, command.TargetID)
	if err != nil {
		return result, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		result.Message = "Deployment target files already removed"
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("deployment target directory: %w", err)
	}
	workDir, composePath, found, err := targetComposePaths(root, command.WorkingDir, command.ComposeFile)
	if err != nil {
		return result, err
	}
	if found {
		environment, err := composeEnvironment(command.Environment, command.PublicURL)
		if err != nil {
			return result, err
		}
		output, composeErr := d.compose(ctx, workDir, environment, projectName(command.TargetID), composePath, "down", "--volumes", "--remove-orphans")
		result.Content = truncate(output, maximumProcessLogSize)
		if composeErr != nil {
			result.State = "failed"
			result.Message = "Docker Compose project cleanup failed"
			return result, fmt.Errorf("docker compose delete: %w: %s", composeErr, output)
		}
	}
	// The snapshots that name the retained image tags are deleted with the
	// files, so the tags have to go first. Images are never force-removed.
	tagFailures := d.removeTargetImageTags(ctx, root)
	if err := os.RemoveAll(root); err != nil {
		result.State = "failed"
		result.Message = "Deployment release cleanup failed"
		return result, fmt.Errorf("delete deployment target files: %w", err)
	}
	result.Message = "Docker Compose project and deployment files deleted"
	if len(tagFailures) > 0 {
		result.Message += "; some retained release images could not be removed"
		result.Content = strings.TrimSpace(result.Content + "\n" + truncate(strings.Join(tagFailures, "\n"), maximumProcessLogSize))
	}
	return result, nil
}

// removeTargetImageTags removes the retained image tags of every release of a
// deployment target and returns the tags that could not be removed.
func (d *Deployer) removeTargetImageTags(ctx context.Context, root string) []string {
	releasesRoot := filepath.Join(root, "releases")
	entries, err := os.ReadDir(releasesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{fmt.Sprintf("list deployment releases: %v", err)}
	}
	var failures []string
	for _, entry := range entries {
		if entry.IsDir() {
			failures = append(failures, d.removeReleaseImageTags(ctx, filepath.Join(releasesRoot, entry.Name()))...)
		}
	}
	return failures
}

func deploymentTargetRoot(releaseRoot, targetID string) (string, error) {
	if releaseRoot == "" || !filepath.IsAbs(releaseRoot) {
		return "", errors.New("release root must be absolute")
	}
	if !safeID(targetID) {
		return "", errors.New("invalid deployment target identifier")
	}
	root := filepath.Join(filepath.Clean(releaseRoot), targetID)
	if !within(filepath.Clean(releaseRoot), root) || root == filepath.Clean(releaseRoot) {
		return "", errors.New("deployment target directory escapes release root")
	}
	return root, nil
}

func targetComposePaths(root, workingDir, composeFile string) (string, string, bool, error) {
	currentLink := filepath.Join(root, "current")
	if _, err := os.Lstat(currentLink); err == nil {
		current, err := filepath.EvalSymlinks(currentLink)
		if err != nil {
			return "", "", false, fmt.Errorf("resolve current release: %w", err)
		}
		if !within(root, current) {
			return "", "", false, errors.New("current release points outside the target release root")
		}
		workDir, composePath, err := composePaths(current, workingDir, composeFile)
		if err != nil {
			return "", "", false, err
		}
		if info, err := os.Stat(composePath); err == nil && !info.IsDir() {
			return workDir, composePath, true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", false, fmt.Errorf("inspect current release: %w", err)
	}

	releasesRoot := filepath.Join(root, "releases")
	entries, err := os.ReadDir(releasesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("list deployment releases: %w", err)
	}
	var latestWorkDir, latestComposePath string
	var latestModTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		release := filepath.Join(releasesRoot, entry.Name())
		workDir, composePath, pathErr := composePaths(release, workingDir, composeFile)
		if pathErr != nil {
			return "", "", false, pathErr
		}
		info, statErr := os.Stat(composePath)
		if statErr != nil || info.IsDir() {
			continue
		}
		if latestComposePath == "" || info.ModTime().After(latestModTime) {
			latestWorkDir = workDir
			latestComposePath = composePath
			latestModTime = info.ModTime()
		}
	}
	return latestWorkDir, latestComposePath, latestComposePath != "", nil
}

func composeEnvironment(values map[string]string, publicURL string) ([]string, error) {
	merged := make(map[string]string, len(values)+1)
	for key, value := range values {
		merged[key] = value
	}
	if port := publicPort(publicURL); port > 0 {
		merged["APP_PORT"] = strconv.Itoa(port)
	}
	environment := make([]string, 0, len(merged))
	for key, value := range merged {
		if strings.ContainsAny(key, "=\x00") {
			return nil, fmt.Errorf("invalid environment key %q", key)
		}
		environment = append(environment, key+"="+value)
	}
	return environment, nil
}

func publicPort(value string) int {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" {
		return 0
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err == nil && value > 0 && value <= 65535 {
			return value
		}
		return 0
	}
	return 0
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

type composeProcess struct {
	Publishers []composePublisher `json:"Publishers"`
}

type composePublisher struct {
	URL           string `json:"URL"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

func composePublicURL(serverAddress, output string) string {
	processes, err := decodeComposeProcesses(output)
	if err != nil {
		return ""
	}
	for _, process := range processes {
		for _, publisher := range process.Publishers {
			if publisher.PublishedPort <= 0 || !strings.EqualFold(publisher.Protocol, "tcp") || isLoopbackPublisher(publisher.URL) {
				continue
			}
			return publicURLForPort(serverAddress, publisher.PublishedPort)
		}
	}
	return ""
}

func composePublishesPort(output string, port int) bool {
	processes, err := decodeComposeProcesses(output)
	if err != nil {
		return false
	}
	for _, process := range processes {
		for _, publisher := range process.Publishers {
			if publisher.PublishedPort == port && strings.EqualFold(publisher.Protocol, "tcp") && !isLoopbackPublisher(publisher.URL) {
				return true
			}
		}
	}
	return false
}

func decodeComposeProcesses(output string) ([]composeProcess, error) {
	var processes []composeProcess
	if err := json.Unmarshal([]byte(output), &processes); err == nil {
		return processes, nil
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	for {
		var process composeProcess
		if err := decoder.Decode(&process); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	return processes, nil
}

func isLoopbackPublisher(value string) bool {
	host := strings.Trim(strings.TrimSpace(value), "[]")
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func publicURLForPort(serverAddress string, port int) string {
	scheme, host := publicURLHost(serverAddress)
	if host == "" {
		return ""
	}
	if port == 443 && scheme == "http" {
		scheme = "https"
	}
	if scheme == "http" && port == 80 || scheme == "https" && port == 443 {
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func publicURLHost(serverAddress string) (string, string) {
	value := strings.TrimSpace(serverAddress)
	if value == "" {
		return "", ""
	}
	if net.ParseIP(strings.Trim(value, "[]")) != nil {
		return "http", strings.Trim(value, "[]")
	}
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "//" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Hostname() == "" {
		return "", ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		scheme = "http"
	}
	return scheme, parsed.Hostname()
}

func run(ctx context.Context, environment []string, command string, args ...string) (string, error) {
	return runIn(ctx, "", environment, command, args...)
}

func runIn(ctx context.Context, directory string, environment []string, command string, args ...string) (string, error) {
	process := childprocess.CommandContext(ctx, command, args...)
	process.Dir = directory
	process.Env = append(os.Environ(), environment...)
	output, err := childprocess.CombinedOutput(process)
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
