package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/codexconfig"
	"github.com/wio-platform/wio/internal/gitidentity"
	"github.com/wio-platform/wio/internal/hostplatform"
)

// installPortable is shared by SSH and command enrollment on macOS/Windows.
// SSH transfers only a temporary installer and feeds its plan through stdin.
func installPortable(ctx context.Context, request InstallRequest) (InstallResult, error) {
	if !hostplatform.Supported(runtime.GOOS, runtime.GOARCH) || !hostplatform.Administrator() {
		return InstallResult{}, ErrPrivilegeRequired
	}
	layout := hostplatform.Current()
	request.report("inspecting", 0, 0)
	previous, loadErr := agent.LoadConfig(layout.Config)
	if loadErr == nil {
		if request.ExpectedServerID == "" || previous.ServerID != request.ExpectedServerID {
			return InstallResult{}, ErrServerMismatch
		}
		if !request.PreserveAccessSettings {
			return InstallResult{}, ErrAccessSettings
		}
		if err := validatePortableRepair(layout, previous); err != nil {
			return InstallResult{}, err
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return InstallResult{}, ErrAccessSettings
	}
	// Repair never silently moves an existing installation's working data.
	state := layout.State
	clone := filepath.Join(state, "projects")
	if loadErr == nil {
		state = previous.StateDir
		clone = previous.CloneRoot
	}
	request.report("preparing_account", 0, 0)
	if err := preparePortableService(ctx, layout); err != nil {
		return InstallResult{}, err
	}
	for _, dir := range []string{state, clone, filepath.Join(state, ".Codex")} {
		if err := managedDirectory(dir); err != nil {
			return InstallResult{}, err
		}
	}
	if err := prepareBinaryDirectory(filepath.Dir(layout.Binary)); err != nil {
		return InstallResult{}, err
	}
	if err := protectPortableState(ctx, layout, state); err != nil {
		return InstallResult{}, err
	}
	request.report("uploading_agent", 0, 0)
	exe, err := os.Executable()
	if err != nil {
		return InstallResult{}, err
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		return InstallResult{}, err
	}
	if err := atomicPortableWrite(layout.Binary, binary, 0755); err != nil {
		return InstallResult{}, err
	}
	request.report("verifying_dependencies", 0, 0)
	warnings := []string{}
	if err := ensurePortableNode(ctx); err != nil {
		warnings = append(warnings, "node_install_failed")
	}
	request.report("checking_cli_versions", 0, 0)
	versions := request.Versions
	if versions.Codex == "" {
		versions, err = New("").latestCLIVersions(ctx)
		if err != nil {
			return InstallResult{}, err
		}
	}
	request.report("configuring_codex", 0, 0)
	keyFile := layout.Key
	if loadErr == nil {
		keyFile = previous.CodexAPIKeyFile
	}
	if err := atomicPortableWrite(keyFile, []byte(request.CodexAPIKey+"\n"), 0600); err != nil {
		return InstallResult{}, err
	}
	configFile := filepath.Join(state, ".Codex", "config.toml")
	old, err := os.ReadFile(configFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, err
	}
	merged, err := codexconfig.Merge(old, request.CodexAPIURL, request.CodexModel)
	if err != nil {
		return InstallResult{}, err
	}
	if err := atomicPortableWrite(configFile, merged, 0600); err != nil {
		return InstallResult{}, err
	}
	if request.GitEndpoint != "" {
		request.report("configuring_git", 0, 0)
		credential, err := gitCredential(request.GitEndpoint, request.GitUsername, request.GitToken)
		if err != nil {
			return InstallResult{}, err
		}
		file := filepath.Join(state, ".git-credentials")
		config, err := gitidentity.Configuration(request.GitCommitName, request.GitCommitEmail, filepath.ToSlash(file))
		if err != nil {
			return InstallResult{}, err
		}
		if err := atomicPortableWrite(file, []byte(credential+"\n"), 0600); err != nil {
			return InstallResult{}, err
		}
		if err := atomicPortableWrite(filepath.Join(state, ".gitconfig"), []byte(config), 0600); err != nil {
			return InstallResult{}, err
		}
	}
	// Dependencies extracted in a private administrator temp directory can retain
	// that ACL when moved into Program Files. Normalize permissions before the
	// unprivileged CLI installation service starts, including newly written keys.
	if err := protectPortableState(ctx, layout, state); err != nil {
		return InstallResult{}, err
	}
	request.report("installing_codex", 0, 0)
	cliWarnings, err := installPortableCLIs(ctx, state, versions)
	if err != nil {
		return InstallResult{}, err
	}
	warnings = append(warnings, cliWarnings...)

	request.report("installing_git", 0, 0)
	if err := ensurePortableGit(ctx, request.ControlURL); err != nil {
		warnings = append(warnings, "git_unavailable")
	}
	request.report("enrolling_agent", 0, 0)
	options := agent.EnrollmentOptions{RegistrationID: request.RegistrationID, ControlURL: request.ControlURL, EnrollmentToken: request.EnrollmentToken, ConfigPath: layout.Config, StateDir: state, CloneRoot: clone, CodexPath: "codex", DockerPath: "docker"}
	if loadErr == nil {
		options.PreviousConfig = &previous
	}
	config, err := agent.Enroll(ctx, options)
	if err != nil {
		return InstallResult{}, errors.New("Agent enrollment was rejected")
	}
	if request.ExpectedServerID != "" && config.ServerID != request.ExpectedServerID {
		return InstallResult{}, ErrServerMismatch
	}

	if err := protectPortableState(ctx, layout, state); err != nil {
		return InstallResult{}, err
	}
	hostname, _ := os.Hostname()
	result := InstallResult{ServerID: config.ServerID, Hostname: hostname, OS: runtime.GOOS, Architecture: runtime.GOARCH, Warnings: warnings}
	request.report("starting_service", 0, 0)
	if err := startPortableService(ctx, layout); err != nil {
		result.Warnings = append(result.Warnings, "service_start_failed")
	}
	return result, nil
}

func atomicPortableWrite(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return errors.New("managed file must be regular")
	}
	file, err := os.CreateTemp(filepath.Dir(name), ".wio-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return hostplatform.ReplaceFile(file.Name(), name)
}
func portableCommand(ctx context.Context, name string, args ...string) (string, error) {
	return portableCommandInDirectory(ctx, "", name, args...)
}

func portableCommandInDirectory(ctx context.Context, directory, name string, args ...string) (string, error) {
	cmd := childprocess.CommandContext(childprocess.WithScope(ctx, childprocess.Installation), name, args...)
	cmd.Dir = directory
	cmd.Env = childprocess.Environment(childprocess.WithScope(ctx, childprocess.Installation), "HOME="+hostplatform.Current().Home, "USERPROFILE="+hostplatform.Current().Home, "APPDATA="+filepath.Join(hostplatform.Current().Home, "AppData", "Roaming"), "LOCALAPPDATA="+filepath.Join(hostplatform.Current().Home, "AppData", "Local"))
	output, err := childprocess.CombinedOutput(cmd)
	if err != nil {
		return "", fmt.Errorf("installation command %s failed", filepath.Base(name))
	}
	return strings.TrimSpace(string(output)), nil
}

// Reject redirected managed roots before publishing any private file.
func managedDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	resolved, resolveErr := filepath.EvalSymlinks(dir)
	if resolveErr != nil {
		return resolveErr
	}
	if !hostplatform.Equal(resolved, dir) {
		return errors.New("managed directory must not traverse a link")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed directory must not be a link")
	}
	return nil
}

// A service-owned config must never make an elevated repair chown or replace
// arbitrary host files. Custom locations inside the managed tree are preserved.
func validatePortableRepair(layout hostplatform.Layout, previous agent.Config) error {
	for _, name := range []string{previous.StateDir, previous.CloneRoot, previous.CodexAPIKeyFile} {
		if !hostplatform.Contains(layout.State, name) {
			return ErrAccessSettings
		}
	}
	return nil
}

func prepareBinaryDirectory(dir string) error {
	// Executables need traversable parents for the dedicated service account.
	// Existing directories retain their administrator-selected permissions.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return managedDirectory(dir)
}
