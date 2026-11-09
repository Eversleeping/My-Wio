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
	t.Cleanup(func() { _ = client.codex.Close() })
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
	t.Cleanup(func() { _ = client.codex.Close() })
	err := client.configureCredentials(protocol.ConfigureCredentialsCommand{CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "bad\nsecret", CodexModel: "gpt-5.6-sol", RemoveGit: true})
	if err == nil {
		t.Fatal("invalid credential command was accepted")
	}
	key, _ := os.ReadFile(keyFile)
	if string(key) != "old-secret\n" {
		t.Fatalf("invalid update changed the key: %q", key)
	}
}

func TestGitOnlyCredentialsDoNotReadOrReconfigureCodex(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".codex", "config.toml")
	if err := os.WriteFile(config, []byte("[invalid Codex config"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No Codex adapter or API key exists. Git must work independently of both.
	client := &Client{config: Config{StateDir: root, CodexAPIKeyFile: filepath.Join(root, "absent.key")}}
	command := protocol.ConfigureCredentialsCommand{Scope: "git", GitCredentials: []protocol.GitCredential{{Endpoint: "https://github.com", Username: "user", Token: "test-token", CommitName: "User", CommitEmail: "user@example.com"}}}
	if err := client.configureCredentials(command); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git-credentials")); err != nil {
		t.Fatal(err)
	}
	if err := client.configureCredentials(protocol.ConfigureCredentialsCommand{Scope: "git", RemoveGit: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git-credentials")); !os.IsNotExist(err) {
		t.Fatal("Git removal failed")
	}
	if data, _ := os.ReadFile(config); string(data) != "[invalid Codex config" {
		t.Fatal("Git changed Codex configuration")
	}
	if _, err := os.Stat(client.config.CodexAPIKeyFile); !os.IsNotExist(err) {
		t.Fatal("Git created a Codex key")
	}
}

func TestCodexOnlyCredentialsPreserveGit(t *testing.T) {
	root := t.TempDir()
	gitFiles := []string{filepath.Join(root, ".gitconfig"), filepath.Join(root, ".git-credentials")}
	for _, path := range gitFiles {
		if err := os.WriteFile(path, []byte("preserve Git file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := NewClient(Config{StateDir: root, CodexAPIKeyFile: filepath.Join(root, "codex.key")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = client.codex.Close() })
	if err := client.configureCredentials(protocol.ConfigureCredentialsCommand{Scope: "codex", CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "test-codex-secret", CodexModel: "gpt-5.6-sol"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range gitFiles {
		if data, _ := os.ReadFile(path); string(data) != "preserve Git file" {
			t.Fatal("Codex changed Git credentials")
		}
	}
	if data, _ := os.ReadFile(client.config.CodexAPIKeyFile); string(data) != "test-codex-secret\n" {
		t.Fatal("Codex key was not applied")
	}
}

func TestConfigureCredentialsWritesMultipleGitCredentials(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "codex.key")
	client := NewClient(Config{CodexAPIKeyFile: keyFile, StateDir: filepath.Join(root, "state")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = client.codex.Close() })
	command := protocol.ConfigureCredentialsCommand{
		CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "new-codex-secret", CodexModel: "gpt-5.6-sol",
		GitCredentials: []protocol.GitCredential{
			{Endpoint: "https://gitee.com", Username: "gitee-user", Token: "gitee-token", CommitName: "Gitee User", CommitEmail: "gitee@example.com"},
			{Endpoint: "https://github.com", Username: "github-user", Token: "github-token", CommitName: "GitHub User", CommitEmail: "github@example.com"},
		},
	}
	if err := client.configureCredentials(command); err != nil {
		t.Fatal(err)
	}
	credentials, err := os.ReadFile(filepath.Join(root, "state", ".git-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(credentials)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two Git credential entries, got %q", credentials)
	}
	for index, expected := range command.GitCredentials {
		parsed, err := url.Parse(lines[index])
		if err != nil {
			t.Fatal(err)
		}
		password, _ := parsed.User.Password()
		if parsed.Host != strings.TrimPrefix(expected.Endpoint, "https://") || parsed.User.Username() != expected.Username || password != expected.Token {
			t.Fatalf("unexpected Git credential entry: %q", lines[index])
		}
	}
	config, err := os.ReadFile(filepath.Join(root, "state", ".gitconfig"))
	if err != nil || !strings.Contains(string(config), `name = "Gitee User"`) || !strings.Contains(string(config), `email = "gitee@example.com"`) {
		t.Fatalf("first Git profile should define the default commit identity: %q %v", config, err)
	}
}

func TestReplaceCredentialFileAtomicallyPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.key")
	if err := os.WriteFile(path, []byte("original-key"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := replaceCredentialFile(path, []byte("replacement-key")); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(original)
	if err != nil || string(old) != "original-key" {
		t.Fatalf("old inode was modified: %q %v", old, err)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "replacement-key" {
		t.Fatalf("replacement missing: %q %v", current, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions changed: %v %v", info, err)
	}
}

func TestReplaceCredentialFileDoesNotTruncateWhenDirectoryIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.key")
	if err := os.WriteFile(path, []byte("original-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := replaceCredentialFile(path, []byte("replacement-key")); err == nil {
		t.Fatal("write unexpectedly succeeded without an atomic replacement")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original-key" {
		t.Fatalf("failed replacement damaged original: %q %v", data, err)
	}
}

func TestLegacyCodexKeyUpdatesUseStateDirAcrossRestart(t *testing.T) {
	config := Config{StateDir: t.TempDir(), CodexAPIKeyFile: "/etc/wio-agent/codex.key"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(config, log)
	defer client.codex.Close()
	command := protocol.ConfigureCredentialsCommand{Scope: "codex", CodexAPIURL: "https://api.example.com/v1", CodexAPIKey: "updated-state-secret", CodexModel: "gpt-5.6-sol"}
	if err := client.configureCredentials(command); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(config.StateDir, "codex.key"))
	if err != nil || string(data) != command.CodexAPIKey+"\n" {
		t.Fatalf("managed key missing: %q %v", data, err)
	}
	env := codexEnvironment(config, log)
	if len(env) != 1 || env[0] != "WIO_CODEX_API_KEY="+command.CodexAPIKey {
		t.Fatalf("restart did not read updated key: %v", env)
	}
}
