package deployer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const releaseImagesFile = ".wio-release-images.json"

var imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var servicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type pinnedService struct {
	Image      string `json:"image"`
	PullPolicy string `json:"pull_policy"`
}
type releaseImages struct {
	Services map[string]pinnedService `json:"services"`
}

func (d *Deployer) saveReleaseImages(ctx context.Context, release, directory string, environment []string, project, composeFile string) (err error) {
	ids, err := d.compose(ctx, directory, environment, project, composeFile, "ps", "--all", "--quiet")
	if err != nil {
		return fmt.Errorf("list release containers: %w", err)
	}
	containers := strings.Fields(ids)
	if len(containers) == 0 {
		return errors.New("release has no containers to snapshot")
	}
	args := append([]string{"inspect", "--format", `{{index .Config.Labels "com.docker.compose.service"}} {{.Image}}`, "--"}, containers...)
	output, err := run(ctx, nil, d.Docker, args...)
	if err != nil {
		return fmt.Errorf("inspect release images: %w", err)
	}
	snapshot, err := parseReleaseImages(output)
	if err != nil {
		return err
	}
	// Keep historical images tagged so routine dangling-image pruning cannot
	// silently remove the artifacts needed for rollback.
	var tagged []string
	defer func() {
		if err == nil {
			return
		}
		// Without a recorded snapshot nothing would ever remove these tags.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		for _, tag := range tagged {
			_ = d.removeImageTag(cleanup, tag)
		}
	}()
	for service, pinned := range snapshot.Services {
		tag := releaseImageTag(release, service)
		if _, err := run(ctx, nil, d.Docker, "image", "tag", pinned.Image, tag); err != nil {
			return fmt.Errorf("retain release image: %w", err)
		}
		tagged = append(tagged, tag)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(release, ".wio-images-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(release, releaseImagesFile))
}

// removeReleaseImageTags removes the tags that keep a release's images
// available for rollback and returns the ones that could not be removed.
// Tags that no longer exist are ignored. Images are never force-removed, so
// an image that is still in use only loses the tag once nothing else needs it.
func (d *Deployer) removeReleaseImageTags(ctx context.Context, release string) []string {
	tags, err := releaseImageTags(release)
	if err != nil {
		return []string{fmt.Sprintf("release %s: %v", filepath.Base(release), err)}
	}
	var failures []string
	for _, tag := range tags {
		if err := d.removeImageTag(ctx, tag); err != nil {
			failures = append(failures, err.Error())
		}
	}
	return failures
}

// releaseImageTags lists the retained image tags recorded by a release's
// snapshot. Releases without a snapshot have none.
func releaseImageTags(release string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(release, releaseImagesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot releaseImages
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("read release image snapshot: %w", err)
	}
	tags := make([]string, 0, len(snapshot.Services))
	for service := range snapshot.Services {
		if servicePattern.MatchString(service) {
			tags = append(tags, releaseImageTag(release, service))
		}
	}
	sort.Strings(tags)
	return tags, nil
}

func (d *Deployer) removeImageTag(ctx context.Context, tag string) error {
	output, err := run(ctx, nil, d.Docker, "image", "rm", tag)
	if err == nil || imageNotFound(output) {
		return nil
	}
	return fmt.Errorf("remove image tag %s: %w: %s", tag, err, output)
}

func imageNotFound(output string) bool {
	message := strings.ToLower(output)
	return strings.Contains(message, "no such image") || strings.Contains(message, "image not known")
}

func releaseImageTag(release, service string) string {
	digest := sha256.Sum256([]byte(release))
	serviceDigest := sha256.Sum256([]byte(service))
	return "wio-release:" + hex.EncodeToString(digest[:8]) + "-" + hex.EncodeToString(serviceDigest[:8])
}

func parseReleaseImages(output string) (releaseImages, error) {
	snapshot := releaseImages{Services: map[string]pinnedService{}}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !servicePattern.MatchString(fields[0]) || !imageIDPattern.MatchString(fields[1]) {
			return snapshot, errors.New("invalid release image metadata")
		}
		if previous, ok := snapshot.Services[fields[0]]; ok && previous.Image != fields[1] {
			return snapshot, errors.New("service replicas use different images")
		}
		snapshot.Services[fields[0]] = pinnedService{Image: fields[1], PullPolicy: "never"}
	}
	if len(snapshot.Services) == 0 {
		return snapshot, errors.New("release image snapshot is empty")
	}
	return snapshot, nil
}

func loadReleaseImages(release string) (releaseImages, error) {
	var snapshot releaseImages
	data, err := os.ReadFile(filepath.Join(release, releaseImagesFile))
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, errors.New("this release has no pinned image snapshot; deploy its Git revision again before rolling back")
	}
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	if len(snapshot.Services) == 0 {
		return snapshot, errors.New("release image snapshot is empty")
	}
	for service, pinned := range snapshot.Services {
		if !servicePattern.MatchString(service) || !imageIDPattern.MatchString(pinned.Image) || pinned.PullPolicy != "never" {
			return snapshot, errors.New("invalid pinned release image")
		}
	}
	return snapshot, nil
}

func (d *Deployer) pinnedCompose(ctx context.Context, release, directory string, environment []string, project, composeFile string, args ...string) (string, error) {
	snapshot, err := loadReleaseImages(release)
	if err != nil {
		return "", err
	}
	if len(args) > 0 && args[0] == "up" {
		for service, pinned := range snapshot.Services {
			if _, err := run(ctx, nil, d.Docker, "image", "inspect", pinned.Image); err != nil {
				return "", fmt.Errorf("historical image for %s is unavailable: %w", service, err)
			}
		}
		args = append(args, "--no-build", "--pull", "never")
	}
	base := []string{"compose", "--project-name", project, "--file", composeFile, "--file", filepath.Join(release, releaseImagesFile)}
	return runIn(ctx, directory, environment, d.Docker, append(base, args...)...)
}
