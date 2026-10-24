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

func validateGeminiCredentials(command protocol.ConfigureGeminiCredentialsCommand) error {
	parsed, err := url.Parse(command.Endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("Gemini API endpoint is invalid")
	}
	if !validCredentialValue(command.APIKey) || !credentialModelPattern.MatchString(command.Model) {
		return errors.New("Gemini API key or model is invalid")
	}
	return nil
}

func (c *Client) geminiCredentialEnvironment(command protocol.ConfigureGeminiCredentialsCommand) []string {
	return []string{
		"GEMINI_API_KEY=" + command.APIKey,
		"GOOGLE_GEMINI_BASE_URL=" + command.Endpoint,
		"GOOGLE_GENAI_USE_GCA=false", "GOOGLE_GENAI_USE_VERTEXAI=false", "GOOGLE_API_KEY=",
		"GEMINI_CLI_AUTH_OVERRIDE=gemini-api-key",
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(c.config.StateDir, "gemini", "api-settings.json"),
	}
}

func (c *Client) configureGeminiCredentials(command protocol.ConfigureGeminiCredentialsCommand) error {
	command.Endpoint = strings.TrimRight(strings.TrimSpace(command.Endpoint), "/")
	command.Model = strings.TrimSpace(command.Model)
	if err := validateGeminiCredentials(command); err != nil {
		return err
	}
	dir := filepath.Join(c.config.StateDir, "gemini")
	return c.gemini.ReconfigureEnvironment(c.geminiCredentialEnvironment(command), command.Model, func() error {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return errors.New("could not create Gemini configuration directory")
		}
		paths := []string{filepath.Join(dir, "api-settings.json"), filepath.Join(dir, "api.json")}
		snapshots, err := snapshotCredentialFiles(paths)
		if err != nil {
			return errors.New("could not read Gemini configuration")
		}
		raw, _ := json.Marshal(command)
		data := [][]byte{[]byte(`{"security":{"auth":{"selectedType":"gemini-api-key","enforcedType":"gemini-api-key"}}}`), raw}
		for i, path := range paths {
			if err := replaceCredentialFile(path, data[i]); err != nil {
				restoreCredentialFiles(snapshots)
				return errors.New("could not save Gemini configuration")
			}
		}
		return nil
	})
}

func (c *Client) loadGeminiCredentials() error {
	raw, err := os.ReadFile(filepath.Join(c.config.StateDir, "gemini", "api.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("could not read Gemini configuration")
	}
	var command protocol.ConfigureGeminiCredentialsCommand
	if json.Unmarshal(raw, &command) != nil {
		return errors.New("invalid Gemini configuration")
	}
	return c.configureGeminiCredentials(command)
}
