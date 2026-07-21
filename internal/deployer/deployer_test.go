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
	err := deployer.Deploy(context.Background(), command, func(status, message, resolved, content string) {
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
