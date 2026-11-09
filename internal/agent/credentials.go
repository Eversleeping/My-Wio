package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wio-platform/wio/internal/codexconfig"
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
	if command.Scope != "" && command.Scope != "codex" && command.Scope != "git" {
		return errors.New("invalid credential update scope")
	}
	updateCodex, updateGit := command.Scope != "git", command.Scope != "codex"
	command.CodexAPIURL = strings.TrimRight(strings.TrimSpace(command.CodexAPIURL), "/")
	command.CodexModel = strings.TrimSpace(command.CodexModel)
	var gitCredentials []protocol.GitCredential
	var err error
	if updateGit {
		gitCredentials, err = normalizedGitCredentials(command)
		if err != nil {
			return err
		}
	}
	if err := validateCredentialCommand(command, gitCredentials); err != nil {
		return err
	}
	codexPaths := []string{codexAPIKeyPath(c.config), filepath.Join(c.config.StateDir, ".codex", "config.toml")}
	gitPaths := []string{filepath.Join(c.config.StateDir, ".git-credentials"), filepath.Join(c.config.StateDir, ".gitconfig")}
	var paths []string
	if updateCodex {
		paths = append(paths, codexPaths...)
	}
	if updateGit {
		paths = append(paths, gitPaths...)
	}
	snapshots, err := snapshotCredentialFiles(paths)
	if err != nil {
		return err
	}
	changes := map[string]*string{}
	if updateCodex {
		configuration, err := codexconfig.Merge(snapshots[1].data, command.CodexAPIURL, command.CodexModel)
		if err != nil {
			return err
		}
		changes[codexPaths[0]] = pointer(command.CodexAPIKey + "\n")
		changes[codexPaths[1]] = pointer(string(configuration))
	}
	if updateGit {
		if command.RemoveGit {
			changes[gitPaths[0]], changes[gitPaths[1]] = nil, nil
		} else {
			lines := make([]string, 0, len(gitCredentials))
			for _, credential := range gitCredentials {
				parsed, _ := url.Parse(credential.Endpoint)
				parsed.User = url.UserPassword(credential.Username, credential.Token)
				lines = append(lines, parsed.String())
			}
			configuration, err := gitidentity.Configuration(gitCredentials[0].CommitName, gitCredentials[0].CommitEmail, gitPaths[0])
			if err != nil {
				return err
			}
			changes[gitPaths[0]] = pointer(strings.Join(lines, "\n") + "\n")
			changes[gitPaths[1]] = pointer(configuration)
		}
	}
	apply := func() error {
		if err := os.MkdirAll(c.config.StateDir, 0o700); err != nil {
			return err
		}
		if updateCodex {
			if err := os.MkdirAll(filepath.Dir(codexPaths[1]), 0o700); err != nil {
				return err
			}
		}
		for _, path := range paths {
			value := changes[path]
			if value == nil {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					restoreCredentialFiles(snapshots)
					return fmt.Errorf("remove %s: %w", path, err)
				}
			} else if err := replaceCredentialFile(path, []byte(*value)); err != nil {
				restoreCredentialFiles(snapshots)
				return fmt.Errorf("write %s: %w", path, err)
			}
		}
		return nil
	}
	if !updateCodex {
		return apply()
	}
	return c.codex.ReconfigureEnvironment([]string{"WIO_CODEX_API_KEY=" + command.CodexAPIKey}, apply)
}

func normalizedGitCredentials(command protocol.ConfigureCredentialsCommand) ([]protocol.GitCredential, error) {
	if command.RemoveGit {
		if len(command.GitCredentials) != 0 || command.GitEndpoint != "" || command.GitUsername != "" || command.GitToken != "" || command.GitCommitName != "" || command.GitCommitEmail != "" {
			return nil, errors.New("Git credentials must be empty when removal is requested")
		}
		return nil, nil
	}
	credentials := append([]protocol.GitCredential(nil), command.GitCredentials...)
	if len(credentials) == 0 {
		credentials = append(credentials, protocol.GitCredential{Endpoint: command.GitEndpoint, Username: command.GitUsername, Token: command.GitToken, CommitName: command.GitCommitName, CommitEmail: command.GitCommitEmail})
	}
	seen := make(map[string]bool, len(credentials))
	for index := range credentials {
		credential := &credentials[index]
		credential.Endpoint = strings.TrimRight(strings.TrimSpace(credential.Endpoint), "/")
		credential.Username = strings.TrimSpace(credential.Username)
		credential.CommitName = strings.TrimSpace(credential.CommitName)
		credential.CommitEmail = strings.TrimSpace(credential.CommitEmail)
		key := credential.Endpoint + "\x00" + credential.Username
		if seen[key] {
			return nil, errors.New("Git credentials must not repeat the same endpoint and username")
		}
		seen[key] = true
	}
	return credentials, nil
}

func validateCredentialCommand(command protocol.ConfigureCredentialsCommand, gitCredentials []protocol.GitCredential) error {
	if command.Scope != "git" {
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
	}
	if command.Scope == "codex" {
		return nil
	}
	if command.RemoveGit {
		return nil
	}
	if len(gitCredentials) == 0 {
		return errors.New("at least one Git credential is required")
	}
	for _, credential := range gitCredentials {
		parsed, err := url.Parse(credential.Endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("Git credential endpoint is invalid")
		}
		if credential.Username == "" || strings.ContainsAny(credential.Username, "\r\n\x00") || !validCredentialValue(credential.Token) {
			return errors.New("Git credentials are invalid")
		}
		if _, _, err := gitidentity.Normalize(credential.CommitName, credential.CommitEmail); err != nil {
			return err
		}
	}
	return nil
}

func validCredentialValue(value string) bool {
	return len(value) >= 8 && len(value) <= 16<<10 && !strings.ContainsAny(value, "\r\n\x00")
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

// Legacy enrollment stores the bootstrap key in a root-owned directory. Keep
// subsequent keys in StateDir so replacement is atomic as the service user.
func codexAPIKeyPath(config Config) string {
	if config.StateDir != "" && (config.CodexAPIKeyFile == "" || config.CodexAPIKeyFile == "/etc/wio-agent/codex.key") {
		return filepath.Join(config.StateDir, "codex.key")
	}
	return config.CodexAPIKeyFile
}

func replaceCredentialFile(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wio-credential-*")
	if err != nil {
		return err // Never truncate the original when atomic replacement is unavailable.
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func pointer(value string) *string { return &value }
