package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/wio-platform/wio/internal/gitidentity"
	"github.com/wio-platform/wio/internal/protocol"
)

var credentialModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type credentialFileSnapshot struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func (c *Client) configureCredentials(command protocol.ConfigureCredentialsCommand) error {
	command.CodexAPIURL = strings.TrimRight(strings.TrimSpace(command.CodexAPIURL), "/")
	command.CodexModel = strings.TrimSpace(command.CodexModel)
	command.GitEndpoint = strings.TrimRight(strings.TrimSpace(command.GitEndpoint), "/")
	command.GitUsername = strings.TrimSpace(command.GitUsername)
	command.GitCommitName = strings.TrimSpace(command.GitCommitName)
	command.GitCommitEmail = strings.TrimSpace(command.GitCommitEmail)
	if err := validateCredentialCommand(command); err != nil {
		return err
	}
	gitCredential := ""
	if !command.RemoveGit {
		parsed, _ := url.Parse(command.GitEndpoint)
		parsed.User = url.UserPassword(command.GitUsername, command.GitToken)
		gitCredential = parsed.String() + "\n"
	}
	paths := []string{
		c.config.CodexAPIKeyFile,
		filepath.Join(c.config.StateDir, ".codex", "config.toml"),
		filepath.Join(c.config.StateDir, ".git-credentials"),
		filepath.Join(c.config.StateDir, ".gitconfig"),
	}
	snapshots, err := snapshotCredentialFiles(paths)
	if err != nil {
		return err
	}
	apply := func() error {
		if err := os.MkdirAll(filepath.Join(c.config.StateDir, ".codex"), 0o700); err != nil {
			return err
		}
		changes := map[string]*string{
			paths[0]: pointer(command.CodexAPIKey + "\n"),
			paths[1]: pointer(codexConfiguration(command.CodexAPIURL, command.CodexModel)),
		}
		if command.RemoveGit {
			changes[paths[2]] = nil
			changes[paths[3]] = nil
		} else {
			changes[paths[2]] = pointer(gitCredential)
			gitConfig, err := gitidentity.Configuration(command.GitCommitName, command.GitCommitEmail, paths[2])
			if err != nil {
				return err
			}
			changes[paths[3]] = pointer(gitConfig)
		}
		for _, path := range paths {
			value := changes[path]
			if value == nil {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					restoreCredentialFiles(snapshots)
					return fmt.Errorf("remove %s: %w", path, err)
				}
				continue
			}
			if err := replaceCredentialFile(path, []byte(*value)); err != nil {
				restoreCredentialFiles(snapshots)
				return fmt.Errorf("write %s: %w", path, err)
			}
		}
		return nil
	}
	return c.codex.ReconfigureEnvironment([]string{"WIO_CODEX_API_KEY=" + command.CodexAPIKey}, apply)
}

func validateCredentialCommand(command protocol.ConfigureCredentialsCommand) error {
	parsed, err := url.Parse(command.CodexAPIURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("Codex API endpoint is invalid")
	}
	if !credentialModelPattern.MatchString(command.CodexModel) {
		return errors.New("Codex model is invalid")
	}
	if !validCredentialValue(command.CodexAPIKey) {
		return errors.New("Codex API key is invalid")
	}
	if command.RemoveGit {
		if command.GitEndpoint != "" || command.GitUsername != "" || command.GitToken != "" || command.GitCommitName != "" || command.GitCommitEmail != "" {
			return errors.New("Git credentials must be empty when removal is requested")
		}
		return nil
	}
	parsed, err = url.Parse(command.GitEndpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Git credential endpoint is invalid")
	}
	if command.GitUsername == "" || strings.ContainsAny(command.GitUsername, "\r\n\x00") || !validCredentialValue(command.GitToken) {
		return errors.New("Git credentials are invalid")
	}
	if _, _, err := gitidentity.Normalize(command.GitCommitName, command.GitCommitEmail); err != nil {
		return err
	}
	return nil
}

func validCredentialValue(value string) bool {
	return len(value) >= 8 && len(value) <= 16<<10 && !strings.ContainsAny(value, "\r\n\x00")
}

func codexConfiguration(apiURL, model string) string {
	return fmt.Sprintf("model = %s\nmodel_provider = \"wio_api\"\nmodel_supports_reasoning_summaries = true\n\n[model_providers.wio_api]\nname = \"Wio API\"\nbase_url = %s\nenv_key = \"WIO_CODEX_API_KEY\"\nwire_api = \"responses\"\n", strconv.Quote(model), strconv.Quote(apiURL))
}

func snapshotCredentialFiles(paths []string) ([]credentialFileSnapshot, error) {
	snapshots := make([]credentialFileSnapshot, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, credentialFileSnapshot{path: path})
			continue
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, credentialFileSnapshot{path: path, data: data, mode: info.Mode().Perm(), exists: true})
	}
	return snapshots, nil
}

func restoreCredentialFiles(snapshots []credentialFileSnapshot) {
	for _, snapshot := range snapshots {
		if snapshot.exists {
			_ = replaceCredentialFile(snapshot.path, snapshot.data)
			_ = os.Chmod(snapshot.path, snapshot.mode)
		} else {
			_ = os.Remove(snapshot.path)
		}
	}
}

func replaceCredentialFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".wio-credential-*")
	if err == nil {
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err = temporary.Write(data); err == nil {
			err = temporary.Chmod(0o600)
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temporaryPath, path)
		}
		return err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return err
	}
	file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if openErr != nil {
		return openErr
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(path, 0o600)
}

func pointer(value string) *string { return &value }
