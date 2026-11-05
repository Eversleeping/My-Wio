package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeCredentialsPersistAndInvalidUpdatePreservesConfiguration(t *testing.T) {
	c := &Client{config: Config{StateDir: t.TempDir()}}
	beforeLogin, err := c.claudeEnvironment()
	if err != nil || len(beforeLogin) != 1 || beforeLogin[0] != "DISABLE_AUTOUPDATER=1" {
		t.Fatalf("unmanaged login: %v %v", beforeLogin, err)
	}
	command := protocol.ConfigureClaudeCredentialsCommand{Endpoint: "https://example.test/gateway/", APIKey: "synthetic-secret", Model: "sonnet"}
	if err := c.configureClaudeCredentials(command); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.config.StateDir, "claude", "api.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("unprotected credential file")
	}
	env, err := c.claudeEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, entry := range []string{"ANTHROPIC_API_KEY=synthetic-secret", "ANTHROPIC_BASE_URL=https://example.test/gateway", "ANTHROPIC_AUTH_TOKEN="} {
		if !strings.Contains(joined, entry) {
			t.Fatalf("missing managed environment entry %q", strings.Split(entry, "=")[0])
		}
	}
	command.APIKey = "bad\nsecret"
	if err := c.configureClaudeCredentials(command); err == nil {
		t.Fatal("invalid secret accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid update changed credentials")
	}
	c.claudeMu.RLock()
	if err := c.configureClaudeCredentials(protocol.ConfigureClaudeCredentialsCommand{}); err == nil {
		t.Fatal("running Claude task allowed credentials change")
	}
	if err := c.installCLI(context.Background(), protocol.CLIInstallCommand{Kind: "claude", Version: "2.1.318"}); err == nil {
		t.Fatal("running Claude task allowed version change")
	}
	c.claudeMu.RUnlock()
}

func TestClaudeInstallVerifiesBeforeSwitchingAndRestoresVersion(t *testing.T) {
	originalRun, originalFind, originalRead := runCodexUpdateCommand, findCodexUpdateCommand, readClaudeUpdateVersion
	t.Cleanup(func() {
		runCodexUpdateCommand, findCodexUpdateCommand, readClaudeUpdateVersion = originalRun, originalFind, originalRead
	})
	c := &Client{config: Config{StateDir: t.TempDir()}}
	findCodexUpdateCommand = func(string) (string, error) { return "npm", nil }
	var args []string
	runCodexUpdateCommand = func(_ context.Context, _ string, values ...string) error { args = values; return nil }
	readClaudeUpdateVersion = func(context.Context, string) string { return "2.1.317 (Claude Code)" }
	command := protocol.CLIInstallCommand{Kind: "claude", Version: "2.1.318"}
	if err := c.installCLI(context.Background(), command); err == nil || effectiveClaudePath(c.config.StateDir) != "claude" {
		t.Fatal("unverified version activated")
	}
	readClaudeUpdateVersion = func(context.Context, string) string { return "2.1.318 (Claude Code)" }
	if err := c.installCLI(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if args[len(args)-1] != "@anthropic-ai/claude-code@2.1.318" {
		t.Fatalf("wrong package: %v", args)
	}
	exe := managedClaudeExecutable(c.config.StateDir, command.Version)
	if err := os.MkdirAll(filepath.Dir(exe), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	if effectiveClaudePath(c.config.StateDir) != exe {
		t.Fatal("managed version was not restored")
	}
}
