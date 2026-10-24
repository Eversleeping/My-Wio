package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	client      *http.Client
	url         string
	tagPrefix   string
	registryURL string
	cacheMu     sync.Mutex
	cache       []string
	cacheLimit  int
	cacheUntil  time.Time
}

func NewReleaseChecker(client *http.Client, url string) *ReleaseChecker {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if strings.TrimSpace(url) == "" {
		url = ReleaseListURL
	}
	return NewTaggedReleaseChecker(client, url, "rust-v")
}

// NewTaggedReleaseChecker shares bounded GitHub release discovery with other
// official CLIs whose stable releases use a different tag prefix.
func NewTaggedReleaseChecker(client *http.Client, url, tagPrefix string) *ReleaseChecker {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	registryURL := ""
	switch url {
	case ReleaseListURL:
		registryURL = "https://registry.npmjs.org/@openai%2fcodex"
	case "https://api.github.com/repos/google-gemini/gemini-cli/releases":
		registryURL = "https://registry.npmjs.org/@google%2fgemini-cli"
	}
	return &ReleaseChecker{client: client, url: url, tagPrefix: tagPrefix, registryURL: registryURL}
}

func (c *ReleaseChecker) LatestStable(ctx context.Context) (string, error) {
	versions, err := c.RecentStable(ctx, 1)
	if err != nil {
		return "", err
	}
	return versions[0], nil
}

// RecentStable returns the newest formal Rust-tagged releases in descending
// semantic-version order. Preview, draft, and non-Rust tags are ignored.
func (c *ReleaseChecker) RecentStable(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	c.cacheMu.Lock()
	if time.Now().Before(c.cacheUntil) && limit <= c.cacheLimit {
		versions := append([]string(nil), c.cache...)
		c.cacheMu.Unlock()
		if len(versions) > limit {
			versions = versions[:limit]
		}
		return versions, nil
	}
	c.cacheMu.Unlock()
	versions, err := c.recentGitHub(ctx, limit)
	if err != nil && c.registryURL != "" {
		versions, err = c.recentNPM(ctx, limit)
	}
	if err != nil {
		return nil, err
	}
	c.cacheMu.Lock()
	c.cache, c.cacheLimit, c.cacheUntil = append([]string(nil), versions...), limit, time.Now().Add(5*time.Minute)
	c.cacheMu.Unlock()
	return versions, nil
}

// The official npm package is a second release source when GitHub is
// unavailable or includes unusually large release notes/assets.
func (c *ReleaseChecker) recentNPM(ctx context.Context, limit int) ([]string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.registryURL, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	response, err := c.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("load official npm releases: %s", response.Status)
	}
	const maxBytes = 16 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBytes {
		return nil, errors.New("npm release metadata is too large")
	}
	var metadata struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	versions := []string{}
	for version := range metadata.Versions {
		if ValidTargetVersion(version) {
			versions = append(versions, version)
		}
	}
	if len(versions) == 0 {
		return nil, ErrNoStableRelease
	}
	sort.Slice(versions, func(i, j int) bool { return versionGreater(versions[i], versions[j]) })
	if len(versions) > limit {
		versions = versions[:limit]
	}
	return versions, nil
}

func (c *ReleaseChecker) recentGitHub(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{})
	versions := make([]string, 0, limit)
	for page := 1; page <= githubReleaseMaxPages; page++ {
		releases, err := c.loadPage(ctx, page)
		if err != nil {
			return nil, err
		}
		for _, release := range releases {
			version := strings.TrimPrefix(release.TagName, c.tagPrefix)
			ok := !release.Draft && !release.Prerelease && strings.HasPrefix(release.TagName, c.tagPrefix) && ValidTargetVersion(version)
			if !ok {
				continue
			}
			if _, ok := seen[version]; ok {
				continue
			}
			seen[version] = struct{}{}
			versions = append(versions, version)
		}
		if len(versions) >= limit || len(releases) < githubReleasePageSize {
			break
		}
	}
	if len(versions) == 0 {
		return nil, ErrNoStableRelease
	}
	sort.Slice(versions, func(i, j int) bool { return versionGreater(versions[i], versions[j]) })
	if len(versions) > limit {
		versions = versions[:limit]
	}
	return versions, nil
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

func stableVersion(release githubRelease) (string, bool) {
	if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "rust-v") {
		return "", false
	}
	version := strings.TrimPrefix(release.TagName, "rust-v")
	return version, ValidTargetVersion(version)
}

func versionGreater(left, right string) bool {
	return UpdateAvailable(right, left)
}
