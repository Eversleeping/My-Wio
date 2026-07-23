package agent

import (
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestConfigureCredentialsWritesProtectedFilesAndRemovesGit(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "etc", "codex.key")
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte("old-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(Config{CodexAPIKeyFile: keyFile, StateDir: filepath.Join(root, "state")}, log)
	command := protocol.ConfigureCredentialsCommand{
		CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "new-codex-secret", CodexModel: "gpt-5.6-sol",
		GitEndpoint: "https://gitee.com", GitUsername: "user@example.com", GitToken: "token:/with spaces", GitCommitName: "Example User", GitCommitEmail: "user@users.noreply.github.com",
	}
	if err := os.MkdirAll(filepath.Join(root, "state", ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state", ".codex", "config.toml"), []byte("web_search = 'live'\n\n[projects.'/srv/project']\ntrust_level = 'trusted'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.configureCredentials(command); err != nil {
		t.Fatal(err)
	}
	paths := []string{keyFile, filepath.Join(root, "state", ".codex", "config.toml"), filepath.Join(root, "state", ".git-credentials"), filepath.Join(root, "state", ".gitconfig")}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("credential file %s is not protected: %v %v", path, info, err)
		}
	}
	key, _ := os.ReadFile(keyFile)
	config, _ := os.ReadFile(paths[1])
	credential, _ := os.ReadFile(paths[2])
	gitConfig, _ := os.ReadFile(paths[3])
	parsed, err := url.Parse(strings.TrimSpace(string(credential)))
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if string(key) != "new-codex-secret\n" || !strings.Contains(string(config), "base_url = 'https://api.example.com/v1'") || !strings.Contains(string(config), "sandbox_mode = 'danger-full-access'") || !strings.Contains(string(config), "network_access = true") || !strings.Contains(string(config), "web_search = 'live'") || !strings.Contains(string(config), "trust_level = 'trusted'") || parsed.User.Username() != command.GitUsername || password != command.GitToken || !strings.Contains(string(gitConfig), `name = "Example User"`) || !strings.Contains(string(gitConfig), `email = "user@users.noreply.github.com"`) {
		t.Fatalf("unexpected credential files: key=%q config=%q credential=%q gitconfig=%q", key, config, credential, gitConfig)
	}
	if err := client.configureCredentials(protocol.ConfigureCredentialsCommand{CodexAPIURL: command.CodexAPIURL, CodexAPIKey: command.CodexAPIKey, CodexModel: command.CodexModel, RemoveGit: true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths[2:] {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Git credential file was not removed: %s %v", path, err)
		}
	}
}

func TestInvalidCredentialCommandLeavesExistingFilesUnchanged(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "codex.key")
	if err := os.WriteFile(keyFile, []byte("old-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{CodexAPIKeyFile: keyFile, StateDir: filepath.Join(root, "state")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := client.configureCredentials(protocol.ConfigureCredentialsCommand{CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "bad\nsecret", CodexModel: "gpt-5.6-sol", RemoveGit: true})
	if err == nil {
		t.Fatal("invalid credential command was accepted")
	}
	key, _ := os.ReadFile(keyFile)
	if string(key) != "old-secret\n" {
		t.Fatalf("invalid update changed the key: %q", key)
	}
}
