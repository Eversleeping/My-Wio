package deployer

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

var (
	newImageID      = "sha256:" + strings.Repeat("a", 64)
	previousImageID = "sha256:" + strings.Repeat("b", 64)
)

// installFakeDeployTools puts fake git and docker executables first on PATH.
// Every docker invocation is appended to the returned log; environment
// variables make individual steps fail.
func installFakeDeployTools(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Compose deployment execution is supported only on Linux")
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "git"), `#!/bin/sh
case "$1" in
  clone) mkdir -p "$5"; echo cloned ;;
  -C)
    echo "$*" >> "$GIT_LOG"
    if [ "$3" = "fetch" ]; then echo fetched
    elif [ "$3" = "checkout" ]; then echo checked-out
    elif [ "$3" = "rev-parse" ]; then echo abc123
    fi ;;
esac`)
	docker := filepath.Join(bin, "docker")
	writeExecutable(t, docker, `#!/bin/sh
echo "$*" >> "$DOCKER_LOG"
case "$1" in
  inspect) echo "app `+newImageID+`" ;;
  image)
    if [ "$2" = inspect ] && [ -n "$FAIL_IMAGE_INSPECT" ]; then echo "Error: No such image" >&2; exit 1; fi
    if [ "$2" = rm ] && [ -n "$IMAGE_RM_ERROR" ]; then echo "$IMAGE_RM_ERROR" >&2; exit 1; fi
    exit 0 ;;
  compose)
    case "$*" in
      *" build") if [ -n "$FAIL_BUILD" ]; then echo "build failed" >&2; exit 1; fi; echo built ;;
      *"ps --all --quiet") echo container-1 ;;
      *"ps --format json") echo '[]' ;;
      *) echo compose-output ;;
    esac ;;
esac`)
	logs := t.TempDir()
	t.Setenv("DOCKER_LOG", filepath.Join(logs, "docker.log"))
	t.Setenv("GIT_LOG", filepath.Join(logs, "git.log"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return docker, filepath.Join(logs, "docker.log")
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// createCurrentRelease creates a promoted release with a pinned image
// snapshot, as a successful earlier deployment leaves it.
func createCurrentRelease(t *testing.T, releaseRoot, targetID string) string {
	t.Helper()
	root := filepath.Join(releaseRoot, targetID)
	release := filepath.Join(root, "releases", "deployment-old")
	writeRelease(t, release, map[string]string{"app": previousImageID})
	if err := os.Symlink(release, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	return release
}

func writeRelease(t *testing.T, release string, images map[string]string) {
	t.Helper()
	if err := os.MkdirAll(release, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "compose.yaml"), []byte("services: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := releaseImages{Services: map[string]pinnedService{}}
	for service, image := range images {
		snapshot.Services[service] = pinnedService{Image: image, PullPolicy: "never"}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, releaseImagesFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func deployCommand(releaseRoot string) protocol.DeployCommand {
	// The fake `compose ps` publishes no ports, so the configured public
	// port check fails after `up`.
	return protocol.DeployCommand{DeploymentID: "deployment-new", TargetID: "target-1", Repository: "https://example.com/repo.git", CommitRef: "main", ComposeFile: "compose.yaml", BuildMode: "build", ReleaseRoot: releaseRoot, PublicURL: "http://203.0.113.10:5010"}
}

func runDeploy(t *testing.T, docker string, command protocol.DeployCommand) (string, error) {
	t.Helper()
	release := filepath.Join(command.ReleaseRoot, command.TargetID, "releases", command.DeploymentID)
	var events []string
	err := New(docker, filepath.Join(t.TempDir(), "missing.sock")).Deploy(context.Background(), command, func(status, message, resolved, detectedPublicURL, content string) {
		events = append(events, status+":"+message+":"+content)
		if message == "commit checked out" {
			if err := os.WriteFile(filepath.Join(release, "compose.yaml"), []byte("services: {}"), 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	return strings.Join(events, "\n"), err
}

func assertNoRelease(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failed release was left behind: %v", err)
	}
}

func assertCurrent(t *testing.T, root, want string) {
	t.Helper()
	current, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || current != want {
		t.Fatalf("current release = %q (%v), want %q", current, err, want)
	}
}

func TestDeployFailureAfterStartRestoresPreviousRelease(t *testing.T) {
	docker, dockerLog := installFakeDeployTools(t)
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	previous := createCurrentRelease(t, releaseRoot, "target-1")
	command := deployCommand(releaseRoot)
	failed := filepath.Join(releaseRoot, "target-1", "releases", command.DeploymentID)

	events, err := runDeploy(t, docker, command)
	if err == nil || !strings.Contains(err.Error(), "configured public port 5010 is not published") || !strings.Contains(err.Error(), "the previous release was restored") {
		t.Fatalf("unexpected deployment error: %v", err)
	}
	log := readLog(t, dockerLog)
	started := strings.Index(log, "--file "+filepath.Join(failed, "compose.yaml")+" up -d --remove-orphans --no-build")
	restored := strings.Index(log, "--file "+filepath.Join(previous, "compose.yaml")+" --file "+filepath.Join(previous, releaseImagesFile)+" up -d --remove-orphans --no-build --pull never")
	if started < 0 || restored < started {
		t.Fatalf("previous release was not restored after the failed start:\n%s", log)
	}
	if !strings.Contains(log, "image inspect "+previousImageID) {
		t.Fatalf("restore did not check the pinned image:\n%s", log)
	}
	if !strings.Contains(events, "previous release restored") {
		t.Fatalf("missing restore event:\n%s", events)
	}
	assertNoRelease(t, failed)
	assertCurrent(t, filepath.Join(releaseRoot, "target-1"), previous)
}

func TestDeployFailureWithoutPreviousReleaseStopsFailedRelease(t *testing.T) {
	docker, dockerLog := installFakeDeployTools(t)
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	command := deployCommand(releaseRoot)
	failed := filepath.Join(releaseRoot, "target-1", "releases", command.DeploymentID)

	_, err := runDeploy(t, docker, command)
	if err == nil || !strings.Contains(err.Error(), "failed release was stopped") {
		t.Fatalf("unexpected deployment error: %v", err)
	}
	log := readLog(t, dockerLog)
	if !strings.Contains(log, "--file "+filepath.Join(failed, "compose.yaml")+" down --remove-orphans\n") {
		t.Fatalf("failed release was not stopped:\n%s", log)
	}
	if strings.Contains(log, "--volumes") {
		t.Fatalf("stopping a failed release must keep volumes:\n%s", log)
	}
	assertNoRelease(t, failed)
	if _, err := os.Lstat(filepath.Join(releaseRoot, "target-1", "current")); !os.IsNotExist(err) {
		t.Fatalf("failed release was marked current: %v", err)
	}
}

func TestDeployFailureReportsUnsuccessfulRestore(t *testing.T) {
	docker, dockerLog := installFakeDeployTools(t)
	t.Setenv("FAIL_IMAGE_INSPECT", "yes")
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	previous := createCurrentRelease(t, releaseRoot, "target-1")
	command := deployCommand(releaseRoot)

	_, err := runDeploy(t, docker, command)
	if err == nil || !strings.Contains(err.Error(), "restoring the previous release failed") || !strings.Contains(err.Error(), "historical image for app is unavailable") {
		t.Fatalf("unexpected deployment error: %v", err)
	}
	if log := readLog(t, dockerLog); strings.Contains(log, "--file "+filepath.Join(previous, releaseImagesFile)) {
		t.Fatalf("restore started Compose without the pinned image:\n%s", log)
	}
	assertNoRelease(t, filepath.Join(releaseRoot, "target-1", "releases", command.DeploymentID))
	assertCurrent(t, filepath.Join(releaseRoot, "target-1"), previous)
}

func TestDeployBuildFailureLeavesRunningReleaseUntouched(t *testing.T) {
	docker, dockerLog := installFakeDeployTools(t)
	t.Setenv("FAIL_BUILD", "yes")
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	previous := createCurrentRelease(t, releaseRoot, "target-1")
	command := deployCommand(releaseRoot)

	_, err := runDeploy(t, docker, command)
	if err == nil || !strings.Contains(err.Error(), "docker compose build") || strings.Contains(err.Error(), "restor") {
		t.Fatalf("unexpected deployment error: %v", err)
	}
	if log := readLog(t, dockerLog); strings.Contains(log, " up ") || strings.Contains(log, " down") {
		t.Fatalf("a build failure changed the running project:\n%s", log)
	}
	assertNoRelease(t, filepath.Join(releaseRoot, "target-1", "releases", command.DeploymentID))
	assertCurrent(t, filepath.Join(releaseRoot, "target-1"), previous)
}

func TestDeployFailureRemovesImageTagsOfFailedRelease(t *testing.T) {
	docker, dockerLog := installFakeDeployTools(t)
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	command := deployCommand(releaseRoot)
	command.PublicURL = ""
	root := filepath.Join(releaseRoot, "target-1")
	failed := filepath.Join(root, "releases", command.DeploymentID)
	// A directory in the way of the new current link makes the promotion,
	// the last step after the image snapshot, fail.
	if err := os.MkdirAll(filepath.Join(root, "current.new", "blocker"), 0o750); err != nil {
		t.Fatal(err)
	}

	_, err := runDeploy(t, docker, command)
	if err == nil || !strings.Contains(err.Error(), "failed release was stopped") {
		t.Fatalf("unexpected deployment error: %v", err)
	}
	tag := releaseImageTag(failed, "app")
	log := readLog(t, dockerLog)
	if !strings.Contains(log, "image tag "+newImageID+" "+tag+"\n") || !strings.Contains(log, "image rm "+tag+"\n") {
		t.Fatalf("retained image tag of the failed release was not removed:\n%s", log)
	}
	assertNoRelease(t, failed)
}

func TestDeployNeverRemovesAnExistingRelease(t *testing.T) {
	docker, _ := installFakeDeployTools(t)
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	existing := createCurrentRelease(t, releaseRoot, "target-1")
	command := deployCommand(releaseRoot)
	command.DeploymentID = filepath.Base(existing)

	if _, err := runDeploy(t, docker, command); err == nil || !strings.Contains(err.Error(), "release already exists") {
		t.Fatalf("unexpected deployment error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(existing, releaseImagesFile)); err != nil {
		t.Fatalf("existing release was modified: %v", err)
	}
}

func TestDeployPassesRefAfterEndOfOptions(t *testing.T) {
	docker, _ := installFakeDeployTools(t)
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	command := deployCommand(releaseRoot)
	command.PublicURL = ""
	command.CommitRef = "release/2026-11"
	if _, err := runDeploy(t, docker, command); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(releaseRoot, "target-1", "releases", command.DeploymentID)
	if log := readLog(t, os.Getenv("GIT_LOG")); !strings.Contains(log, "-C "+release+" fetch --depth=1 -- origin release/2026-11\n") {
		t.Fatalf("unexpected fetch invocation:\n%s", log)
	}
}

func TestDeployRejectsUnsafeRefsBeforeRunningGit(t *testing.T) {
	docker, dockerLog := installFakeDeployTools(t)
	releaseRoot := filepath.Join(t.TempDir(), "releases")
	for _, sourceType := range []string{"remote", "workspace"} {
		for _, ref := range []string{"--upload-pack=touch /tmp/p; git-upload-pack", "-c", "main\n", "main --orphan", "HEAD~1", "a..b", "+main", "refs/heads/*"} {
			command := deployCommand(releaseRoot)
			command.SourceType = sourceType
			command.SourcePath = t.TempDir()
			command.CommitRef = ref
			if _, err := runDeploy(t, docker, command); err == nil || !strings.Contains(err.Error(), "invalid deployment Git ref") {
				t.Fatalf("%s ref %q was accepted: %v", sourceType, ref, err)
			}
		}
	}
	if log := readLog(t, os.Getenv("GIT_LOG")) + readLog(t, dockerLog); log != "" {
		t.Fatalf("tools ran for an invalid ref:\n%s", log)
	}
	if _, err := os.Stat(releaseRoot); !os.IsNotExist(err) {
		t.Fatalf("release directory was created for an invalid ref: %v", err)
	}
}

func TestValidateGitRefMatchesGitCheckRefFormat(t *testing.T) {
	valid := []string{"main", "HEAD", "feature/login", "release/2026-11", "v1.2.3", "refs/heads/main", "refs/tags/v1", "abc1234", strings.Repeat("0123456789abcdef", 4), "功能/登录", "a-b_c.d", "user@host", "x+y"}
	invalid := []string{"", "-x", "--upload-pack=x", "+main", "a b", "a\tb", "a\nb", "a\x7f", "a..b", "a/.b", ".a", "a.lock", "a/b.lock", "a/", "/a", "a//b", "@", "a@{1}", "a^", "a~1", "a:b", "a?", "a*", "a[b", `a\b`, "a.", "a b", "a　b", strings.Repeat("a", maxGitRefLength+1)}
	for _, ref := range valid {
		if err := ValidateGitRef(ref); err != nil {
			t.Errorf("ValidateGitRef(%q) = %v", ref, err)
		}
	}
	for _, ref := range invalid {
		if err := ValidateGitRef(ref); err == nil {
			t.Errorf("ValidateGitRef(%q) accepted an invalid ref", ref)
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	// The ref name rules must agree with Git's own implementation.
	for _, ref := range append(valid, invalid...) {
		if ref == "" || strings.HasPrefix(ref, "-") || len(ref) > maxGitRefLength {
			continue
		}
		command := exec.Command("git", "check-ref-format", "--allow-onelevel", ref)
		command.Dir = t.TempDir()
		gitAccepts := command.Run() == nil
		if validRefName(ref) != gitAccepts {
			t.Errorf("validRefName(%q) = %v, git check-ref-format = %v", ref, validRefName(ref), gitAccepts)
		}
	}
}

func TestDeleteTargetRemovesRetainedReleaseImages(t *testing.T) {
	for name, rmError := range map[string]string{
		"tags removed":        "",
		"tags already gone":   "Error response from daemon: No such image: wio-release:missing",
		"tag still in use":    "Error response from daemon: conflict: unable to remove repository reference (must force)",
		"podman missing tags": "Error: wio-release:missing: image not known",
	} {
		t.Run(name, func(t *testing.T) {
			docker, dockerLog := installFakeDeployTools(t)
			t.Setenv("IMAGE_RM_ERROR", rmError)
			releaseRoot := filepath.Join(t.TempDir(), "releases")
			targetRoot := filepath.Join(releaseRoot, "target-delete")
			current := filepath.Join(targetRoot, "releases", "deployment-1")
			older := filepath.Join(targetRoot, "releases", "deployment-0")
			legacy := filepath.Join(targetRoot, "releases", "deployment-legacy")
			writeRelease(t, current, map[string]string{"app": newImageID, "worker": newImageID})
			writeRelease(t, older, map[string]string{"app": previousImageID})
			if err := os.MkdirAll(legacy, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(current, filepath.Join(targetRoot, "current")); err != nil {
				t.Fatal(err)
			}

			result, err := New(docker).ContainerAction(context.Background(), protocol.ContainerActionCommand{TargetID: "target-delete", Action: "delete", ReleaseRoot: releaseRoot, ComposeFile: "compose.yaml"})
			if err != nil || result.State != "removed" {
				t.Fatalf("unexpected target deletion result: %#v %v", result, err)
			}
			if _, err := os.Stat(targetRoot); !os.IsNotExist(err) {
				t.Fatalf("target release directory still exists: %v", err)
			}
			log := readLog(t, dockerLog)
			down := strings.Index(log, "down --volumes --remove-orphans")
			for _, tag := range []string{releaseImageTag(current, "app"), releaseImageTag(current, "worker"), releaseImageTag(older, "app")} {
				if index := strings.Index(log, "image rm "+tag+"\n"); index < down {
					t.Fatalf("tag %s was not removed after stopping the project:\n%s", tag, log)
				}
			}
			if strings.Contains(log, "rm -f") || strings.Contains(log, "--force") {
				t.Fatalf("images were force-removed:\n%s", log)
			}
			failed := strings.Contains(rmError, "conflict")
			if reported := strings.Contains(result.Message, "could not be removed"); reported != failed || failed && !strings.Contains(result.Content, "conflict") {
				t.Fatalf("unexpected result for image removal failure %q: %#v", rmError, result)
			}
		})
	}
}
