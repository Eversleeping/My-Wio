package deployer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in: uses a unique Compose project, existing local images and no ports.
func TestPinnedRollbackWithDocker(t *testing.T) {
	docker := os.Getenv("WIO_TEST_DOCKER")
	if docker == "" {
		t.Skip("set WIO_TEST_DOCKER for isolated Docker integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	project := fmt.Sprintf("wio-regression-%d", time.Now().UnixNano())
	tag := project + ":current"
	d := New(docker)
	release1, release2 := t.TempDir(), t.TempDir()
	compose := func(release string) string {
		file := filepath.Join(release, "compose.json")
		content := fmt.Sprintf(`{"services":{"app":{"image":%q,"pull_policy":"never","entrypoint":["/bin/sh","-c"],"command":["sleep 300"]}}}`, tag)
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	first, second := compose(release1), compose(release2)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := d.compose(cleanup, release1, nil, project, first, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Errorf("clean isolated project: %v", err)
		}
		tags := []string{tag}
		for _, release := range []string{release1, release2} {
			if snapshot, err := loadReleaseImages(release); err == nil {
				for service := range snapshot.Services {
					tags = append(tags, releaseImageTag(release, service))
				}
			}
		}
		_, _ = run(cleanup, nil, docker, append([]string{"image", "rm"}, tags...)...)
	})
	for i, release := range []string{release1, release2} {
		base := []string{"postgres:15-alpine", "postgres:16-alpine"}[i]
		if _, err := run(ctx, nil, docker, "image", "tag", base, tag); err != nil {
			t.Fatal(err)
		}
		file := []string{first, second}[i]
		if _, err := d.compose(ctx, release, nil, project, file, "up", "-d", "--force-recreate", "--pull", "never"); err != nil {
			t.Fatal(err)
		}
		if err := d.saveReleaseImages(ctx, release, release, nil, project, file); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := loadReleaseImages(release1)
	newer, _ := loadReleaseImages(release2)
	if old.Services["app"].Image == newer.Services["app"].Image {
		t.Fatal("integration images must differ")
	}
	if _, err := d.pinnedCompose(ctx, release1, release1, nil, project, first, "up", "-d"); err != nil {
		t.Fatal(err)
	}
	ids, err := d.compose(ctx, release1, nil, project, first, "ps", "--all", "--quiet")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := run(ctx, nil, docker, "inspect", "--format", "{{.Image}}", strings.TrimSpace(ids))
	if err != nil || strings.TrimSpace(actual) != old.Services["app"].Image {
		t.Fatalf("rollback used %s rather than %s: %v", actual, old.Services["app"].Image, err)
	}
}
