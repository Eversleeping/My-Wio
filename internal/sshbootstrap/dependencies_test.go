package sshbootstrap

import (
	"errors"
	"strings"
	"testing"
)

func TestBootstrapInstallsMissingDependenciesAndRechecksServiceAccount(t *testing.T) {
	var steps, commands []string
	nodeInstalled, deploymentInstalled := false, false
	probe, warnings := ensureBootstrapDependencies(InstallRequest{Progress: func(progress InstallProgress) { steps = append(steps, progress.Step) }}, inspection{}, func(command string) (string, error) {
		commands = append(commands, command)
		switch command {
		case nodeInstallationScript(officialNPMRegistry):
			nodeInstalled = true
		case agentNodeVerificationScript():
			if !nodeInstalled {
				t.Fatal("Node was checked before installing it")
			}
		case "/usr/local/bin/wio-agent prerequisite-helper --once":
			deploymentInstalled = true
		default:
			if !deploymentInstalled || !strings.HasPrefix(command, "su -s /bin/sh -c") {
				t.Fatalf("unexpected verification: %s", command)
			}
		}
		return "ready", nil
	}, officialNPMRegistry)
	if !probe.NPMReady || !probe.DockerReady || !probe.ComposeReady || !probe.GitReady || len(warnings) != 0 {
		t.Fatalf("freshly installed dependencies were not recognized: %#v %v", probe, warnings)
	}
	if strings.Join(steps, ",") != "installing_node,installing_deployment_dependencies" || len(commands) != 6 {
		t.Fatalf("unexpected installation sequence: %v %v", steps, commands)
	}
}

func TestBootstrapKeepsWorkingDependenciesAndReportsVerificationFailures(t *testing.T) {
	ready := inspection{NPMReady: true, DockerReady: true, ComposeReady: true, GitReady: true}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "unusable_as_agent"}[fail], func(t *testing.T) {
			probe, warnings := ensureBootstrapDependencies(InstallRequest{}, ready, func(command string) (string, error) {
				if !strings.HasPrefix(command, "su -s /bin/sh -c") {
					t.Fatalf("healthy system must not reinstall dependencies: %s", command)
				}
				if fail {
					return "", errors.New("unavailable for service user")
				}
				return "ready", nil
			}, officialNPMRegistry)
			if fail {
				if probe.NPMReady || probe.DockerReady || probe.ComposeReady || probe.GitReady || len(warnings) != 2 {
					t.Fatalf("stale probe was reused: %#v %v", probe, warnings)
				}
			} else if len(warnings) != 0 {
				t.Fatalf("healthy dependencies generated warnings: %v", warnings)
			}
		})
	}
}

func TestBootstrapDoesNotAssumeSuccessfulInstallMeansUsableTools(t *testing.T) {
	probe, warnings := ensureBootstrapDependencies(InstallRequest{}, inspection{}, func(command string) (string, error) {
		if command == nodeInstallationScript(officialNPMRegistry) || strings.Contains(command, "prerequisite-helper --once") {
			return "installed", nil
		}
		return "", errors.New("verification failed")
	}, officialNPMRegistry)
	if probe.NPMReady || probe.DockerReady || len(warnings) != 2 {
		t.Fatalf("verification failure was ignored: %#v %v", probe, warnings)
	}
}

func TestNodeInstallationUsesVerifiedLTSArchives(t *testing.T) {
	script := nodeInstallationScript(officialNPMRegistry)
	for _, expected := range []string{"latest-v22.x", "node_arch=x64", "node_arch=arm64", "sha256sum -c -", "--no-same-owner", "apt-get", "dnf", "yum", "node --version", "npm --version"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("Node setup missing %s", expected)
		}
	}
	if strings.Contains(script, "npmmirror") {
		t.Fatal("international setup unexpectedly uses mainland mirror")
	}
	if !strings.Contains(nodeInstallationScript(mainlandNPMRegistry), "https://registry.npmmirror.com/-/binary/node/latest-v22.x") {
		t.Fatal("mainland Node mirror was not selected")
	}
}
