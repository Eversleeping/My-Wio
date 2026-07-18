package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const ReleaseListURL = "https://api.github.com/repos/openai/codex/releases"

const (
	githubReleasePageSize = 10
	githubReleaseMaxPages = 5
	githubReleaseMaxBytes = 5 << 20
)

var ErrNoStableRelease = errors.New("no stable Codex CLI release was found")

type ReleaseChecker struct {
	client *http.Client
	url    string
}

func NewReleaseChecker(client *http.Client, url string) *ReleaseChecker {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if strings.TrimSpace(url) == "" {
		url = ReleaseListURL
	}
	return &ReleaseChecker{client: client, url: url}
}

func (c *ReleaseChecker) LatestStable(ctx context.Context) (string, error) {
	for page := 1; page <= githubReleaseMaxPages; page++ {
		releases, err := c.loadPage(ctx, page)
		if err != nil {
			return "", err
		}
		latest := latestStableVersion(releases)
		if latest != "" {
			return latest, nil
		}
		if len(releases) < githubReleasePageSize {
			break
		}
	}
	return "", ErrNoStableRelease
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (c *ReleaseChecker) loadPage(ctx context.Context, page int) ([]githubRelease, error) {
	endpoint, err := url.Parse(c.url)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("per_page", strconv.Itoa(githubReleasePageSize))
	query.Set("page", strconv.Itoa(page))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Wio-Controlplane")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("load Codex CLI releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("load Codex CLI releases: GitHub returned %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, githubReleaseMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex CLI releases: %w", err)
	}
	if len(raw) > githubReleaseMaxBytes {
		return nil, errors.New("Codex CLI release response is too large")
	}
	var releases []githubRelease
	if err := json.Unmarshal(raw, &releases); err != nil {
		return nil, fmt.Errorf("decode Codex CLI releases: %w", err)
	}
	return releases, nil
}

func latestStableVersion(releases []githubRelease) string {
	latest := ""
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "rust-v") {
			continue
		}
		version := strings.TrimPrefix(release.TagName, "rust-v")
		if !ValidTargetVersion(version) {
			continue
		}
		if latest == "" || UpdateAvailable(latest, version) {
			latest = version
		}
	}
	return latest
}
