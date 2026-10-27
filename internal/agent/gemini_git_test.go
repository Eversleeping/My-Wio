package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/gitidentity"
)

func TestGeminiBoundGitCredentialsSurviveCLIIsolation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	state := filepath.Join(root, "Agent's state directory")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(state, ".git-credentials")
	if err := os.WriteFile(credentials, []byte("https://synthetic-user:synthetic-github-secret@github.com\nhttps://synthetic-user:synthetic-gitee-secret@gitee.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := gitidentity.Configuration("Dev O'Neil", "dev@example.test", credentials)
	if err != nil {
		t.Fatal(err)
	}
	// These settings must not be restored by the Gemini integration.
	config += "\n[core]\n\thooksPath = forbidden-hooks\n[alias]\n\tunexpected = !echo forbidden\n"
	if err := os.WriteFile(filepath.Join(state, ".gitconfig"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	client := &Client{config: Config{StateDir: state}}
	overrides, err := client.geminiGitEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(overrides, "\n"), "synthetic-github-secret") || strings.Contains(strings.Join(overrides, "\n"), "synthetic-gitee-secret") {
		t.Fatal("token copied into process environment")
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_CONFIG_") {
			env = append(env, entry)
		}
	}
	// Reproduce CLI 0.65's isolation and trailing helper reset. Use a clean
	// working directory to avoid any real repository credential configuration.
	env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=", "GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	run := func(environment []string, input string, args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = environment
		cmd.Stdin = strings.NewReader(input)
		return cmd.Output()
	}
	if _, err := run(env, "protocol=https\nhost=github.com\n\n", "credential", "fill"); err == nil {
		t.Fatal("test did not reproduce missing credentials")
	}
	env = append(env, overrides...)
	for host, secret := range map[string]string{"github.com": "synthetic-github-secret", "gitee.com": "synthetic-gitee-secret"} {
		result, err := run(env, "protocol=https\nhost="+host+"\n\n", "credential", "fill")
		if err != nil || !strings.Contains(string(result), "password="+secret) {
			t.Fatalf("bound credential unavailable for %s", host)
		}
	}
	if _, err := run(env, "protocol=https\nhost=unbound.example\n\n", "credential", "fill"); err == nil {
		t.Fatal("credentials made available for unbound host")
	}
	for key, expected := range map[string]string{"user.name": "Dev O'Neil", "user.email": "dev@example.test", "core.hooksPath": ""} {
		result, err := run(env, "", "config", "--get", key)
		if err != nil || strings.TrimSpace(string(result)) != expected {
			t.Fatalf("unexpected Git setting for %s", key)
		}
	}
	if _, err := run(env, "", "config", "--get", "alias.unexpected"); err == nil {
		t.Fatal("global aliases restored")
	}
}

func TestGeminiMissingOrInvalidGitCredentials(t *testing.T) {
	root := t.TempDir()
	client := &Client{config: Config{StateDir: root}}
	if env, err := client.geminiGitEnvironment(context.Background()); err != nil || len(env) != 0 {
		t.Fatal("missing optional credentials blocked Gemini")
	}
	if err := os.WriteFile(filepath.Join(root, ".git-credentials"), []byte("invalid-secret-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.geminiGitEnvironment(context.Background()); err == nil || strings.Contains(err.Error(), "invalid-secret-value") {
		t.Fatal("invalid credentials accepted or leaked in error")
	}
}
