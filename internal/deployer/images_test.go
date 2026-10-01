package deployer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseImagesRejectInvalidAndMixedReplicas(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	for _, input := range []string{"", "app mutable:latest", "../app " + image, "app " + image + "\napp sha256:" + strings.Repeat("b", 64)} {
		if _, err := parseReleaseImages(input); err == nil {
			t.Fatalf("invalid images accepted: %q", input)
		}
	}
	snapshot, err := parseReleaseImages("app " + image + "\napp " + image)
	if err != nil || snapshot.Services["app"].Image != image {
		t.Fatalf("valid replicas: %v", err)
	}
	if _, err := loadReleaseImages(t.TempDir()); err == nil {
		t.Fatal("legacy release accepted for rollback")
	}
}

func TestPinnedComposeUsesHistoricalImagesAndFailsBeforeStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux command execution")
	}
	release := t.TempDir()
	log := filepath.Join(t.TempDir(), "commands")
	image := "sha256:" + strings.Repeat("a", 64)
	data, _ := json.Marshal(releaseImages{Services: map[string]pinnedService{"app": {Image: image, PullPolicy: "never"}}})
	if err := os.WriteFile(filepath.Join(release, releaseImagesFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(t.TempDir(), "docker")
	writeExecutable(t, docker, `#!/bin/sh
echo "$@" >> "$DOCKER_LOG"
if [ "$1" = image ] && [ "$MISSING_IMAGE" = yes ]; then exit 1; fi
`)
	t.Setenv("DOCKER_LOG", log)
	d := New(docker)
	if _, err := d.pinnedCompose(context.Background(), release, release, nil, "project", filepath.Join(release, "compose.yaml"), "up", "-d"); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(log)
	for _, want := range []string{"image inspect " + image, "--file " + filepath.Join(release, releaseImagesFile), "--no-build --pull never"} {
		if !strings.Contains(string(commands), want) {
			t.Fatalf("missing %s: %s", want, commands)
		}
	}
	if err := os.WriteFile(log, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISSING_IMAGE", "yes")
	if _, err := d.pinnedCompose(context.Background(), release, release, nil, "project", "compose.yaml", "up", "-d"); err == nil {
		t.Fatal("missing historical image accepted")
	}
	commands, _ = os.ReadFile(log)
	if strings.Contains(string(commands), "compose") {
		t.Fatal("started compose with unavailable historical image")
	}
}
