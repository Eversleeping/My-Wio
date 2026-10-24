package agent

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiCredentialsPersistAndInvalidUpdatesPreserveConfiguration(t *testing.T) {
	config := Config{StateDir: t.TempDir(), CodexAPIKeyFile: filepath.Join(t.TempDir(), "codex.key")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := NewClient(config, log)
	defer c.codex.Close()
	command := protocol.ConfigureGeminiCredentialsCommand{Endpoint: "https://generativelanguage.googleapis.com", APIKey: "synthetic-secret", Model: "flash"}
	if err := c.configureGeminiCredentials(command); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.StateDir, "gemini", "api.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("unprotected configuration")
	}
	command.APIKey = "bad\nsecret"
	if err := c.configureGeminiCredentials(command); err == nil {
		t.Fatal("invalid secret accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid update changed configuration")
	}
	restarted := NewClient(config, log)
	defer restarted.codex.Close()
	if err := restarted.loadGeminiCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.CodexAPIKeyFile); !os.IsNotExist(err) {
		t.Fatal("Gemini wrote Codex credentials")
	}
}

func TestExistingGeminiVersionedEndpointIsNormalizedOnRestart(t *testing.T) {
	config := Config{StateDir: t.TempDir()}
	command := protocol.ConfigureGeminiCredentialsCommand{Endpoint: "https://example.test/gateway/v1/", APIKey: "synthetic-secret", Model: "flash"}
	dir := filepath.Join(config.StateDir, "gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(command)
	path := filepath.Join(dir, "api.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer c.codex.Close()
	// NewClient restores the persisted credentials using the same path as a
	// newly applied profile, so older Agents' configuration is migrated too.
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &command) != nil || command.Endpoint != "https://example.test/gateway" || command.APIKey != "synthetic-secret" {
		t.Fatal("existing Gemini endpoint was not migrated")
	}
	env := strings.Join(c.geminiCredentialEnvironment(command), "\n")
	if !strings.Contains(env, "GOOGLE_GEMINI_BASE_URL=https://example.test/gateway\n") || !strings.Contains(env, "GOOGLE_GENAI_API_VERSION=v1beta\n") {
		t.Fatal("CLI API path differs from model discovery")
	}
}
