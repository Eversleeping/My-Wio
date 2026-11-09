package agent

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/protocol"
)

func validateClaudeCredentials(command protocol.ConfigureClaudeCredentialsCommand) error {
	parsed, err := url.Parse(command.Endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("Claude API endpoint is invalid")
	}
	model := strings.TrimSuffix(command.Model, "[1m]")
	if !validCredentialValue(command.APIKey) || len(command.Model) > 128 || !credentialModelPattern.MatchString(model) {
		return errors.New("Claude API key or model is invalid")
	}
	return nil
}

func (c *Client) configureClaudeCredentials(command protocol.ConfigureClaudeCredentialsCommand) error {
	if !c.claudeMu.TryLock() {
		return errors.New("finish active Claude turns before changing credentials")
	}
	defer c.claudeMu.Unlock()
	command.Endpoint = strings.TrimRight(strings.TrimSpace(command.Endpoint), "/")
	command.Model = strings.TrimSpace(command.Model)
	if err := validateClaudeCredentials(command); err != nil {
		return err
	}
	dir := filepath.Join(c.config.StateDir, "claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("could not create Claude configuration directory")
	}
	raw, err := json.Marshal(command)
	if err != nil || replaceCredentialFile(filepath.Join(dir, "api.json"), raw) != nil {
		return errors.New("could not save Claude configuration")
	}
	return nil
}

// claudeEnvironment is called with claudeMu held. With no managed profile,
// the Agent account's own Claude login or environment remains available.
func (c *Client) claudeEnvironment() ([]string, error) {
	environment := []string{"DISABLE_AUTOUPDATER=1"}
	raw, err := os.ReadFile(filepath.Join(c.config.StateDir, "claude", "api.json"))
	if errors.Is(err, os.ErrNotExist) {
		return environment, nil
	}
	if err != nil {
		return nil, errors.New("could not read Claude configuration")
	}
	var command protocol.ConfigureClaudeCredentialsCommand
	if json.Unmarshal(raw, &command) != nil || validateClaudeCredentials(command) != nil {
		return nil, errors.New("invalid Claude configuration")
	}
	return append(environment, "ANTHROPIC_API_KEY="+command.APIKey,
		"ANTHROPIC_BASE_URL="+command.Endpoint, "ANTHROPIC_AUTH_TOKEN=",
		"CLAUDE_CODE_USE_BEDROCK=", "CLAUDE_CODE_USE_VERTEX=", "CLAUDE_CODE_USE_FOUNDRY="), nil
}

// claudeStreamProxy routes a configured custom endpoint through a per-turn
// loopback proxy that ends each streamed response at message_stop. Relays that
// hold the stream open afterwards otherwise delay every Claude step by about
// 30 seconds. If the proxy cannot start, the CLI talks to the endpoint directly.
// Set WIO_CLAUDE_STREAM_PROXY=off on the Agent to disable it.
func (c *Client) claudeStreamProxy(environment []string) ([]string, func()) {
	if os.Getenv("WIO_CLAUDE_STREAM_PROXY") == "off" {
		return environment, func() {}
	}
	for i, entry := range environment {
		endpoint, ok := strings.CutPrefix(entry, "ANTHROPIC_BASE_URL=")
		if !ok || endpoint == "" {
			continue
		}
		// Anthropic's own API closes streams promptly; keep it first-party.
		if parsed, err := url.Parse(endpoint); err != nil || parsed.Hostname() == "anthropic.com" || strings.HasSuffix(parsed.Hostname(), ".anthropic.com") {
			return environment, func() {}
		}
		base, stop, err := claudeadapter.StartStreamProxy(endpoint)
		if err != nil {
			c.log.Warn("Claude stream proxy unavailable; using the endpoint directly", "error", err)
			return environment, func() {}
		}
		routed := append([]string(nil), environment...)
		routed[i] = "ANTHROPIC_BASE_URL=" + base
		// Keep an inherited HTTP(S)_PROXY from capturing loopback traffic.
		for _, key := range []string{"NO_PROXY", "no_proxy"} {
			routed = append(routed, key+"="+strings.TrimPrefix(os.Getenv(key)+",127.0.0.1,localhost", ","))
		}
		return routed, stop
	}
	return environment, func() {}
}
