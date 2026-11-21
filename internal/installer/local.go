package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/wio-platform/wio/deploy"
	"github.com/wio-platform/wio/internal/agent"
	"github.com/wio-platform/wio/internal/codexcli"
	"github.com/wio-platform/wio/internal/hostplatform"
)

// Event is shared by SSH streaming and the local command. Error details from
// child commands are never serialized because they may contain credentials.
type Event struct {
	Type    string         `json:"type"`
	Step    string         `json:"step,omitempty"`
	Current int64          `json:"current,omitempty"`
	Total   int64          `json:"total,omitempty"`
	Code    string         `json:"code,omitempty"`
	Error   string         `json:"error,omitempty"`
	Result  *InstallResult `json:"result,omitempty"`
}

// InstallLocal runs from a temporary executable, then atomically publishes the
// installed binary. Embedded systemd templates match the container assets.
func InstallLocal(ctx context.Context, request InstallRequest) (InstallResult, error) {
	if request.OS != "" && request.OS != runtime.GOOS {
		return InstallResult{}, ErrUnsupportedPlatform
	}
	if err := ValidateConfiguration(request); err != nil {
		return InstallResult{}, err
	}
	previous, err := agent.LoadConfig(hostplatform.Current().Config)
	if err == nil && (request.ExpectedServerID == "" || previous.ServerID != request.ExpectedServerID) {
		return InstallResult{}, ErrServerMismatch
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, ErrAccessSettings
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return InstallResult{}, err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return InstallResult{}, err
	}
	staging, err := os.MkdirTemp("", "wio-assets-")
	if err != nil {
		return InstallResult{}, err
	}
	defer os.RemoveAll(staging)
	for name, data := range map[string][]byte{"wio-agent-linux-" + runtime.GOARCH: binary, "wio-agent.service": deploy.AgentService, "wio-prerequisite.service": deploy.PrerequisiteService} {
		if err := os.WriteFile(filepath.Join(staging, name), data, 0600); err != nil {
			return InstallResult{}, err
		}
	}
	var result InstallResult
	if runtime.GOOS == "linux" {
		result, err = New(staging).installOnHost(ctx, localHost{ctx}, request)
	} else {
		result, err = installPortable(ctx, request)
	}
	if err != nil {
		return result, err
	}
	if request.OnInstalled != nil {
		if err := request.OnInstalled(result); err != nil {
			return result, err
		}
	}
	for _, warning := range result.Warnings {
		if warning == "service_start_failed" || warning == "service_not_active" {
			return result, ErrInstallation
		}
	}
	request.report("waiting_agent", 0, 0)
	if err := verifyInstalledAgent(ctx, request.ControlURL, result.ServerID); err != nil {
		return result, err
	}
	return result, nil
}

func ValidateConfiguration(request InstallRequest) error {
	if !validAPIURL(request.ControlURL) || request.EnrollmentToken == "" || !validAPIURL(request.CodexAPIURL) || !validAPIKey(request.CodexAPIKey) || !validModel(request.CodexModel) {
		return ErrInvalidTarget
	}
	if request.RegistrationID != "" && !validServerID(request.RegistrationID) {
		return ErrInvalidTarget
	}
	if request.GitEndpoint != "" {
		// Reuse existing validation without requiring actual SSH credentials locally.
		check := request
		check.Target = Target{Host: "localhost", Port: 22, User: "root", AuthMethod: "none"}
		check.ExpectedFingerprint = "SHA256:local"
		if err := validateInstallRequest(check); err != nil {
			return err
		}
	}
	for _, version := range []string{request.Versions.Codex, request.Versions.Gemini, request.Versions.Claude} {
		if version != "" && !codexcli.ValidTargetVersion(version) {
			return ErrInvalidTarget
		}
	}
	return nil
}

func (s *Service) launchInstaller(ctx context.Context, client *sshHost, key hostKeyInfo, request InstallRequest) (InstallResult, error) {
	request.report("inspecting", 0, 0)
	probe, err := inspect(client, key)
	if err != nil {
		return InstallResult{}, err
	}
	if request.OS != "" && request.OS != probe.OS {
		return InstallResult{}, ErrUnsupportedPlatform
	}
	if probe.OS == "windows" {
		return s.launchWindowsInstaller(ctx, client, probe, request)
	}
	if !probe.SudoReady {
		return InstallResult{}, ErrPrivilegeRequired
	}
	asset, err := platformAgentAsset(probe.OS, probe.Architecture)
	if err != nil {
		return InstallResult{}, err
	}
	binary, err := os.ReadFile(filepath.Join(s.assetDir, asset))
	if err != nil {
		return InstallResult{}, ErrAssetsUnavailable
	}
	request.report("checking_cli_versions", 0, 0)
	if request.Versions.Codex == "" {
		request.Versions, err = s.latestCLIVersions(ctx)
		if err != nil {
			return InstallResult{}, err
		}
	}
	root := isRoot(client)
	directory, err := runTrimmed(client, elevated(root, "mktemp -d /tmp/wio-install.XXXXXXXXXX"))
	if err != nil {
		return InstallResult{}, ErrInstallation
	}
	if !validStagingDirectory(directory) {
		return InstallResult{}, ErrInstallation
	}
	defer run(client, elevated(root, "rm -rf -- "+shellQuote(directory)))
	request.report("uploading_agent", 0, int64(len(binary)))
	output := &limitedBuffer{remaining: 4096}
	reader := &progressReader{reader: bytes.NewReader(binary), total: int64(len(binary)), notify: func(current, total int64) { request.report("uploading_agent", current, total) }, lastAt: time.Now()}
	script := "umask 077; cat > " + shellQuote(directory+"/wio-agent") + " && chmod 0700 " + shellQuote(directory+"/wio-agent")
	if err := executeCommand(client, elevated(root, script), reader, output); err != nil {
		return InstallResult{}, ErrInstallation
	}
	return launchRemoteSession(client, elevated(root, shellQuote(directory+"/wio-agent")+" install --request-stdin"), request)
}

func launchRemoteSession(client *sshHost, command string, request InstallRequest) (InstallResult, error) {
	// The target needs no SSH login credentials or host fingerprint.
	request.Target = Target{}
	request.ExpectedFingerprint = ""
	raw, err := json.Marshal(request)
	if err != nil {
		return InstallResult{}, err
	}
	session, err := client.NewSession()
	if err != nil {
		return InstallResult{}, err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(raw)
	stdout, err := session.StdoutPipe()
	if err != nil {
		return InstallResult{}, err
	}
	session.Stderr = io.Discard
	if err := session.Start(command); err != nil {
		return InstallResult{}, ErrInstallation
	}
	var result *InstallResult
	decoder := json.NewDecoder(stdout)
	for {
		var event Event
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return InstallResult{}, ErrInstallation
		}
		switch event.Type {
		case "progress":
			request.report(event.Step, event.Current, event.Total)
		case "complete":
			result = event.Result
		case "error":
			if event.Result != nil {
				if event.Code == "agent_heartbeat_pending" {
					return *event.Result, ErrHeartbeatPending
				}
				return *event.Result, ErrInstallation
			}
			return InstallResult{}, fmt.Errorf("%w: target installer failed", ErrInstallation)
		}
	}
	if err := session.Wait(); err != nil {
		return InstallResult{}, ErrInstallation
	}
	if result == nil {
		return InstallResult{}, ErrInstallation
	}
	return *result, nil
}

var stagingPattern = regexp.MustCompile(`^/tmp/wio-install\.[A-Za-z0-9]{10}$`)

func validStagingDirectory(value string) bool { return stagingPattern.MatchString(value) }
