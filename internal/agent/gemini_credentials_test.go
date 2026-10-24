package agent

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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
