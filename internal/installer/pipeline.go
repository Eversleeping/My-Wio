package installer

import (
	"context"
	"fmt"
	"github.com/wio-platform/wio/internal/codexconfig"
	"github.com/wio-platform/wio/internal/gitidentity"
	"os"
	"path/filepath"
	"strings"
)

// installOnHost is the common installation pipeline. Both entry points execute
// this in the target installer; secrets enter through stdin rather than argv.
func (s *Service) installOnHost(ctx context.Context, client commandHost, request InstallRequest) (InstallResult, error) {
	request.report("inspecting", 0, 0)
	probe, err := inspect(client, hostKeyInfo{})
	if err != nil {
		return InstallResult{}, err
	}
	if !strings.EqualFold(probe.OS, "linux") {
		return InstallResult{}, fmt.Errorf("%w: %s", ErrUnsupportedPlatform, probe.OS)
	}
	if !probe.SudoReady {
		return InstallResult{}, ErrPrivilegeRequired
	}

	assetName, err := agentAsset(probe.Architecture)
	if err != nil {
		return InstallResult{}, err
	}
	agentBinary, err := os.ReadFile(filepath.Join(s.assetDir, assetName))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: %v", ErrAssetsUnavailable, err)
	}
	unit, err := os.ReadFile(filepath.Join(s.assetDir, "wio-agent.service"))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: %v", ErrAssetsUnavailable, err)
	}
	prerequisiteUnit, err := os.ReadFile(filepath.Join(s.assetDir, "wio-prerequisite.service"))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: %v", ErrAssetsUnavailable, err)
	}
	request.report("checking_cli_versions", 0, 0)
	versions := request.Versions
	if versions.Codex == "" {
		versions, err = s.latestCLIVersions(ctx)
	}
	if err != nil {
		return InstallResult{}, err
	}

	root := isRoot(client)
	if request.PreserveAccessSettings {
		raw, readErr := run(client, elevated(root, "if [ -f /etc/wio-agent/config.json ]; then cat /etc/wio-agent/config.json; fi"))
		if readErr != nil {
			return InstallResult{}, ErrAccessSettings
		}
		if strings.TrimSpace(raw) != "" {
			previous, _, readErr := decodeAccess([]byte(raw), request.ExpectedServerID)
			if readErr != nil {
				return InstallResult{}, readErr
			}
			request.AllowSudo = previous.AllowSudo
		} else {
			request.AllowSudo = false
		}
	}
	request.report("uploading_agent", 0, int64(len(agentBinary)))
	if err := upload(client, root, "/usr/local/bin/wio-agent", "0755", agentBinary, func(current, total int64) {
		request.report("uploading_agent", current, total)
	}); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install agent binary: %v", ErrInstallation, err)
	}
	unit = agentServiceUnit(unit, request.AllowSudo)
	request.report("uploading_service", 0, int64(len(unit)))
	if err := upload(client, root, "/etc/systemd/system/wio-agent.service", "0644", unit, func(current, total int64) {
		request.report("uploading_service", current, total)
	}); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install systemd unit: %v", ErrInstallation, err)
	}
	request.report("uploading_prerequisite_helper", 0, int64(len(prerequisiteUnit)))
	if err := upload(client, root, "/etc/systemd/system/wio-prerequisite.service", "0644", prerequisiteUnit, func(current, total int64) {
		request.report("uploading_prerequisite_helper", current, total)
	}); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install prerequisite helper: %v", ErrInstallation, err)
	}
	request.report("preparing_account", 0, 0)
	setup := agentSetupScript(request.AllowSudo) + "\nrm -f /etc/systemd/system/wio-agent.service.d/90-wio-access.conf"
	if _, err := run(client, elevated(root, setup)); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not prepare service account", ErrInstallation)
	}
	resultWarnings := make([]string, 0, 10)
	countries, _ := run(client, npmCountryDetectionScript())
	npmRegistry := npmRegistryForCountries(countries)
	probe, dependencyWarnings := ensureBootstrapDependencies(request, probe, func(script string) (string, error) {
		return run(client, elevated(root, script))
	}, npmRegistry)
	resultWarnings = append(resultWarnings, dependencyWarnings...)
	if probe.NPMReady {
		request.report("configuring_npm", 0, 0)
		if _, err := run(client, elevated(root, npmRegistryConfigurationScript(npmRegistry))); err != nil {
			resultWarnings = append(resultWarnings, "npm_registry_configuration_failed")
		}
	}
	request.report("configuring_codex", 0, 0)
	if err := upload(client, root, "/etc/wio-agent/codex.key", "0600", []byte(request.CodexAPIKey+"\n"), nil); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install Codex credentials: %v", ErrInstallation, err)
	}
	existingConfig, err := run(client, elevated(root, "if [ -f /var/lib/wio-agent/.Codex/config.toml ]; then cat /var/lib/wio-agent/.Codex/config.toml; fi"))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not read existing Codex configuration", ErrInstallation)
	}
	configuration, err := codexconfig.Merge([]byte(existingConfig), request.CodexAPIURL, request.CodexModel)
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not merge Codex configuration: %v", ErrInstallation, err)
	}
	if err := upload(client, root, "/var/lib/wio-agent/.Codex/config.toml", "0600", configuration, nil); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not install Codex configuration: %v", ErrInstallation, err)
	}
	if _, err := run(client, elevated(root, "chown wio-agent:wio-agent /var/lib/wio-agent/.Codex/config.toml")); err != nil {
		return InstallResult{}, fmt.Errorf("%w: could not protect Codex configuration", ErrInstallation)
	}
	if request.GitEndpoint != "" {
		request.report("configuring_git", 0, 0)
		credential, err := gitCredential(request.GitEndpoint, request.GitUsername, request.GitToken)
		if err != nil {
			return InstallResult{}, ErrInvalidTarget
		}
		if err := upload(client, root, "/var/lib/wio-agent/.git-credentials", "0600", []byte(credential+"\n"), nil); err != nil {
			return InstallResult{}, fmt.Errorf("%w: could not install Git credentials: %v", ErrInstallation, err)
		}
		gitConfig, err := gitidentity.Configuration(request.GitCommitName, request.GitCommitEmail, "/var/lib/wio-agent/.git-credentials")
		if err != nil {
			return InstallResult{}, ErrInvalidTarget
		}
		if err := upload(client, root, "/var/lib/wio-agent/.gitconfig", "0600", []byte(gitConfig), nil); err != nil {
			return InstallResult{}, fmt.Errorf("%w: could not install Git configuration: %v", ErrInstallation, err)
		}
		if _, err := run(client, elevated(root, "chown wio-agent:wio-agent /var/lib/wio-agent/.git-credentials /var/lib/wio-agent/.gitconfig")); err != nil {
			return InstallResult{}, fmt.Errorf("%w: could not protect Git credentials", ErrInstallation)
		}
	}

	codexReady := probe.CodexReady
	codexPath := "codex"
	if probe.NPMReady {
		request.report("installing_codex", 0, 0)
		managedPath, installErr := installBootstrapCLI("codex", versions.Codex, npmRegistry, func(script string) (string, error) {
			return run(client, elevated(root, script))
		})
		if installErr == nil {
			codexReady = true
			codexPath = managedPath
		} else {
			resultWarnings = append(resultWarnings, "codex_install_failed")
		}
	}
	if !codexReady && !probe.NPMReady {
		resultWarnings = append(resultWarnings, "codex_install_requires_npm")
	}
	if probe.NPMReady {
		request.report("installing_gemini", 0, 0)
		if _, err := installBootstrapCLI("gemini", versions.Gemini, npmRegistry, func(script string) (string, error) {
			return run(client, elevated(root, script))
		}); err != nil {
			resultWarnings = append(resultWarnings, "gemini_install_failed")
		}
	} else {
		resultWarnings = append(resultWarnings, "gemini_install_requires_npm")
	}
	if probe.NPMReady {
		request.report("installing_claude", 0, 0)
		if _, err := installBootstrapCLI("claude", versions.Claude, npmRegistry, func(script string) (string, error) { return run(client, elevated(root, script)) }); err != nil {
			resultWarnings = append(resultWarnings, "claude_install_failed")
		}
	} else {
		resultWarnings = append(resultWarnings, "claude_install_requires_npm")
	}
	if probe.NPMReady {
		request.report("installing_playwright", 0, 0)
		if _, err := run(client, elevated(root, playwrightPackageInstallationScript())); err != nil {
			resultWarnings = append(resultWarnings, "playwright_package_install_failed")
		} else {
			if _, err := run(client, elevated(root, playwrightDependenciesInstallationScript())); err != nil {
				resultWarnings = append(resultWarnings, "playwright_dependencies_install_failed")
			}
			if _, err := run(client, elevated(root, playwrightBrowserInstallationScript(npmRegistry))); err != nil {
				resultWarnings = append(resultWarnings, "playwright_chromium_install_failed")
			} else if _, err := run(client, elevated(root, playwrightVerificationScript())); err != nil {
				resultWarnings = append(resultWarnings, "playwright_verification_failed")
			}
		}
	} else {
		resultWarnings = append(resultWarnings, "playwright_install_requires_npm")
	}

	request.report("enrolling_agent", 0, 0)
	// The token travels on the SSH session's standard input: a command-line
	// argument would be visible to every local user of the target in ps.
	enrollCommand := enrollCommandOptions(request.ControlURL, codexPath, request.AllowSudo)
	if request.RegistrationID != "" {
		enrollCommand += " --registration " + shellQuote(request.RegistrationID)
	}
	if request.PreserveAccessSettings {
		enrollCommand += " --preserve-access-settings"
	}
	output, err := runWithInput(client, elevated(root, enrollCommand), []byte(request.EnrollmentToken+"\n"))
	if err != nil {
		return InstallResult{}, fmt.Errorf("%w: agent enrollment was rejected", ErrInstallation)
	}
	serverID := enrollmentServerID(output)
	if serverID == "" {
		return InstallResult{}, fmt.Errorf("%w: enrollment response was not recognized", ErrInstallation)
	}

	result := InstallResult{ServerID: serverID, Hostname: probe.Hostname, Architecture: normalizedArchitecture(probe.Architecture), Warnings: resultWarnings}
	permissions := "chown wio-agent:wio-agent /etc/wio-agent/config.json /etc/wio-agent/codex.key && chmod 0600 /etc/wio-agent/config.json /etc/wio-agent/codex.key"
	if _, err := run(client, elevated(root, permissions)); err != nil {
		result.Warnings = append(result.Warnings, "config_permission_failed")
	}
	request.report("starting_service", 0, 0)
	start := "systemctl daemon-reload && systemctl enable wio-prerequisite wio-agent && systemctl restart wio-prerequisite wio-agent"
	if _, err := run(client, elevated(root, start)); err != nil {
		result.Warnings = append(result.Warnings, "service_start_failed")
	} else if _, err := run(client, "systemctl is-active --quiet wio-agent"); err != nil {
		result.Warnings = append(result.Warnings, "service_not_active")
	}
	if !probe.DockerReady {
		result.Warnings = append(result.Warnings, "docker_unavailable")
	}
	if !probe.ComposeReady {
		result.Warnings = append(result.Warnings, "docker_compose_unavailable")
	}
	if !probe.GitReady {
		result.Warnings = append(result.Warnings, "git_unavailable")
	}
	if !codexReady {
		result.Warnings = append(result.Warnings, "codex_unavailable")
	}
	return result, nil
}
