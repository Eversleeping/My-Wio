package agent

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

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
