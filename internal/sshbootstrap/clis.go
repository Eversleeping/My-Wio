package sshbootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/wio-platform/wio/internal/codexcli"
)

type cliVersions struct {
	codex  string
	gemini string
}

// Resolve formal releases at enrollment time, using the same official discovery
// and npm fallback as the version manager. Never fall back to a pinned old CLI.
func (s *Service) latestCLIVersions(ctx context.Context) (cliVersions, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	codex, err := s.codexReleases.LatestStable(ctx)
	if err != nil {
		return cliVersions{}, fmt.Errorf("%w: Codex: %v", ErrCLIReleasesUnavailable, err)
	}
	gemini, err := s.geminiReleases.LatestStable(ctx)
	if err != nil {
		return cliVersions{}, fmt.Errorf("%w: Gemini: %v", ErrCLIReleasesUnavailable, err)
	}
	return cliVersions{codex: codex, gemini: gemini}, nil
}

func installBootstrapCLI(kind, version, registry string, run func(string) (string, error)) (string, error) {
	packages := map[string]string{"codex": "@openai/codex", "gemini": "@google/gemini-cli"}
	packageName, ok := packages[kind]
	if !ok || !codexcli.ValidTargetVersion(version) {
		return "", ErrInvalidTarget
	}
	prefix := "/var/lib/wio-agent/" + kind + "/versions/" + version
	executable := prefix + "/node_modules/.bin/" + kind
	asAgent := func(command string) (string, error) {
		return run("su -s /bin/sh -c " + shellQuote(command) + " wio-agent")
	}
	if _, err := asAgent("npm install --prefix " + shellQuote(prefix) + " --omit=dev --registry=" + shellQuote(registry) + " " + shellQuote(packageName+"@"+version)); err != nil {
		return "", fmt.Errorf("install %s CLI: %w", kind, err)
	}
	if reported, err := asAgent(shellQuote(executable) + " --version"); err != nil || !codexcli.ReportedVersionMatches(reported, version) {
		return "", fmt.Errorf("%s CLI version verification failed", kind)
	}
	// Both CLI adapters read this marker on startup. Publish only after verifying
	// the executable, so an existing selection survives an unsuccessful repair.
	marker := "/var/lib/wio-agent/" + kind + "/current-version"
	activate := `set -eu
current=$(mktemp ` + shellQuote(marker+".XXXXXX") + `)
trap 'rm -f "$current"' EXIT
printf '%s\n' ` + shellQuote(version) + ` > "$current"
chmod 0600 "$current"
mv -f "$current" ` + shellQuote(marker)
	if _, err := asAgent(activate); err != nil {
		return "", fmt.Errorf("activate %s CLI: %w", kind, err)
	}
	return executable, nil
}
