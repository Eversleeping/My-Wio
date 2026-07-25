package deployer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestReleasePathIsConstrained(t *testing.T) {
	root := filepath.Join(t.TempDir(), "releases")
	target, release, err := releasePath(root, "target-1", "deploy-1")
	if err != nil {
		t.Fatal(err)
	}
	if !within(target, release) {
		t.Fatalf("release escaped target: %s", release)
	}
	if _, _, err := releasePath(root, "../escape", "deploy-1"); err == nil {
		t.Fatal("unsafe target id was accepted")
	}
}

func TestDeployReportsProcessOutput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Compose deployment execution is supported only on Linux")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "git"), `#!/bin/sh
case "$1" in
  clone) mkdir -p "$5"; echo cloned ;;
  -C)
    if [ "$3" = "fetch" ]; then echo fetched
    elif [ "$3" = "checkout" ]; then echo checked-out
    elif [ "$3" = "rev-parse" ]; then echo abc123
    fi ;;
esac`)
	docker := filepath.Join(bin, "docker")
	writeExecutable(t, docker, "#!/bin/sh\necho compose-output")
	root := filepath.Join(t.TempDir(), "releases")
	command := protocol.DeployCommand{DeploymentID: "deployment-1", TargetID: "target-1", Repository: "https://example.com/repo.git", CommitRef: "main", ComposeFile: "compose.yaml", BuildMode: "build", ReleaseRoot: root}
	var events []string
	deployer := New(docker)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+oldPath)
	err := deployer.Deploy(context.Background(), command, func(status, message, resolved, detectedPublicURL, content string) {
		events = append(events, status+":"+message+":"+content)
		if message == "commit checked out" {
			if err := os.WriteFile(filepath.Join(root, "target-1", "releases", "deployment-1", "compose.yaml"), []byte("services: {}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(events, "\n")
	for _, expected := range []string{"repository cloned:cloned", "commit fetched:fetched", "commit checked out:checked-out", "Docker Compose project started:compose-output", "succeeded:deployment is healthy"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in events:\n%s", expected, joined)
		}
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestComposePathsRejectTraversal(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	if _, _, err := composePaths(release, "../outside", "compose.yaml"); err == nil {
		t.Fatal("unsafe working directory was accepted")
	}
	if _, _, err := composePaths(release, "app", "../../secret"); err == nil {
		t.Fatal("unsafe compose file was accepted")
	}
}

func TestComposePublicURLUsesPublishedTCPPort(t *testing.T) {
	tests := []struct {
		name    string
		address string
		output  string
		want    string
	}{
		{
			name:    "JSON array",
			address: "203.0.113.10",
			output:  `[{"Publishers":[{"URL":"0.0.0.0","TargetPort":3001,"PublishedPort":5010,"Protocol":"tcp"}]}]`,
			want:    "http://203.0.113.10:5010",
		},
		{
			name:    "line delimited JSON and HTTPS",
			address: "https://app.example.com",
			output:  "{\"Publishers\":[{\"URL\":\"::\",\"PublishedPort\":443,\"Protocol\":\"tcp\"}]}\n{\"Publishers\":[]}",
			want:    "https://app.example.com",
		},
		{
			name:    "loopback binding is not external",
			address: "203.0.113.10",
			output:  `[{"Publishers":[{"URL":"127.0.0.1","PublishedPort":8080,"Protocol":"tcp"}]}]`,
			want:    "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := composePublicURL(test.address, test.output); got != test.want {
				t.Fatalf("composePublicURL() = %q, want %q", got, test.want)
			}
		})
	}
	if !composePublishesPort(tests[0].output, 5010) || composePublishesPort(tests[0].output, 3001) {
		t.Fatal("published port validation did not match Compose output")
	}
}

func TestComposeEnvironmentUsesConfiguredPublicPort(t *testing.T) {
	environment, err := composeEnvironment(map[string]string{"APP_PORT": "3001", "TOKEN": "secret"}, "http://203.0.113.10:5010")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "APP_PORT=5010") || strings.Contains(joined, "APP_PORT=3001") || !strings.Contains(joined, "TOKEN=secret") {
		t.Fatalf("unexpected Compose environment: %q", joined)
	}
	for value, want := range map[string]int{"http://203.0.113.10": 0, "https://app.example.com": 0, "": 0} {
		if got := publicPort(value); got != want {
			t.Fatalf("publicPort(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestContainerActionSpecsAreConstrained(t *testing.T) {
	tests := []struct {
		action string
		state  string
		args   string
	}{
		{action: "start", state: "running", args: "up -d --no-build --remove-orphans"},
		{action: "stop", state: "stopped", args: "stop"},
		{action: "restart", state: "running", args: "restart"},
		{action: "remove", state: "removed", args: "down --remove-orphans"},
	}
	for _, test := range tests {
		args, state, message, err := containerActionSpec(test.action)
		if err != nil || state != test.state || strings.Join(args, " ") != test.args || message == "" {
			t.Fatalf("unexpected %s action: args=%q state=%q message=%q err=%v", test.action, strings.Join(args, " "), state, message, err)
		}
		if strings.Contains(strings.Join(args, " "), "--volumes") || strings.Contains(strings.Join(args, " "), " -v") {
			t.Fatalf("%s action unexpectedly deletes volumes: %v", test.action, args)
		}
	}
	if _, _, _, err := containerActionSpec("shell"); err == nil {
		t.Fatal("unsupported container action was accepted")
	}
}

func TestCurrentReleaseValidatesRootAndTarget(t *testing.T) {
	if _, _, err := currentRelease("relative/releases", "target-1"); err == nil {
		t.Fatal("relative release root was accepted")
	}
	if _, _, err := currentRelease(filepath.Join(t.TempDir(), "releases"), "../escape"); err == nil {
		t.Fatal("unsafe target identifier was accepted")
	}
}

func TestDeleteTargetStopsComposeAndRemovesReleaseFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Compose target deletion is supported only on Linux")
	}
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	targetRoot := filepath.Join(releaseRoot, "target-delete")
	release := filepath.Join(targetRoot, "releases", "deployment-failed")
	if err := os.MkdirAll(release, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "compose.yaml"), []byte("services: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	docker := filepath.Join(bin, "docker")
	logPath := filepath.Join(t.TempDir(), "docker.log")
	writeExecutable(t, docker, "#!/bin/sh\necho \"$@\" > \"$DOCKER_LOG\"")
	t.Setenv("DOCKER_LOG", logPath)

	result, err := New(docker).ContainerAction(context.Background(), protocol.ContainerActionCommand{TargetID: "target-delete", Action: "delete", ReleaseRoot: releaseRoot, ComposeFile: "compose.yaml"})
	if err != nil || result.State != "removed" {
		t.Fatalf("unexpected target deletion result: %#v %v", result, err)
	}
	if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
		t.Fatalf("target release directory still exists: %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "down --volumes --remove-orphans") {
		t.Fatalf("Compose project was not stopped: %q %v", log, err)
	}
}

func TestDeploymentTargetRootRejectsUnsafePaths(t *testing.T) {
	if _, err := deploymentTargetRoot("relative/releases", "target-1"); err == nil {
		t.Fatal("relative release root was accepted")
	}
	if _, err := deploymentTargetRoot(filepath.Join(t.TempDir(), "releases"), "../escape"); err == nil {
		t.Fatal("unsafe target identifier was accepted")
	}
}

func TestPreflightStopsBeforeReleaseWhenDockerIsUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Compose deployment execution is supported only on Linux")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "git"), "#!/bin/sh\necho 'git version 2.45.0'")
	docker := filepath.Join(bin, "docker")
	writeExecutable(t, docker, "#!/bin/sh\necho 'daemon unavailable' >&2\nexit 1")
	root := filepath.Join(t.TempDir(), "releases")
	command := protocol.DeployCommand{DeploymentID: "deployment-preflight", TargetID: "target-preflight", SourceType: "remote", Repository: "https://example.com/repo.git", CommitRef: "main", ComposeFile: "compose.yaml", BuildMode: "build", ReleaseRoot: root}
	var events []string
	err := New(docker).Deploy(context.Background(), command, func(status, message, resolved, detectedPublicURL, content string) {
		events = append(events, status+":"+message+":"+content)
	})
	if err == nil || !strings.Contains(err.Error(), "Docker daemon") {
		t.Fatalf("unexpected preflight result: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "target-preflight")); !os.IsNotExist(statErr) {
		t.Fatalf("release directory was created before preflight completed: %v", statErr)
	}
	if !strings.Contains(strings.Join(events, "\n"), "failed:environment check: Docker daemon") {
		t.Fatalf("missing failed preflight event: %#v", events)
	}
}

func TestMissingAutomaticPrerequisite(t *testing.T) {
	if !missingAutomaticPrerequisite([]PreflightCheck{{Name: "Git", OK: false}}) {
		t.Fatal("missing Git should trigger automatic setup")
	}
	if !missingAutomaticPrerequisite([]PreflightCheck{{Name: "Docker daemon", OK: false}}) {
		t.Fatal("missing Docker daemon should trigger automatic setup")
	}
	if missingAutomaticPrerequisite([]PreflightCheck{{Name: "release directory", OK: false}}) {
		t.Fatal("release path failures must not trigger package installation")
	}
}
