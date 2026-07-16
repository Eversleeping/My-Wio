package deployer

import (
	"path/filepath"
	"testing"
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

func TestComposePathsRejectTraversal(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	if _, _, err := composePaths(release, "../outside", "compose.yaml"); err == nil {
		t.Fatal("unsafe working directory was accepted")
	}
	if _, _, err := composePaths(release, "app", "../../secret"); err == nil {
		t.Fatal("unsafe compose file was accepted")
	}
}
