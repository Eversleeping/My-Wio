package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestEnsureControlPlaneAgentTokenPersistsEncryptedValue(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := security.DevVault()
	first, err := ensureControlPlaneAgentToken(context.Background(), database, vault, log)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureControlPlaneAgentToken(context.Background(), database, vault, log)
	if err != nil || second != first {
		t.Fatalf("control-plane Agent token was not stable across restarts: %q %q %v", first, second, err)
	}
	stored, err := database.Setting(context.Background(), store.ControlPlaneAgentTokenKey, "")
	if err != nil || stored == first {
		t.Fatalf("control-plane Agent token was stored in clear text: %q %v", stored, err)
	}
	var decrypted string
	if err := vault.Decrypt(stored, &decrypted); err != nil || decrypted != first {
		t.Fatalf("stored control-plane Agent token could not be decrypted: %q %v", decrypted, err)
	}
}

func TestControlPlaneAgentConfigUsesListenerPort(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WIO_CONTROL_AGENT_URL", "")
	t.Setenv("WIO_CONTROL_AGENT_SCAN_ROOTS", root)
	t.Setenv("WIO_CONTROL_AGENT_CLONE_ROOT", filepath.Join(root, "projects"))
	t.Setenv("WIO_CONTROL_AGENT_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("WIO_CONTROL_AGENT_CODEX_KEY_FILE", filepath.Join(root, "codex.key"))
	t.Setenv("WIO_CONTROL_AGENT_PREREQUISITE_SOCKET", filepath.Join(root, "helper.sock"))
	config, err := controlPlaneAgentConfig("127.0.0.1:42123", "agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if config.ControlURL != "http://127.0.0.1:42123" || config.ServerID != store.ControlPlaneServerID {
		t.Fatalf("unexpected control-plane Agent configuration: %#v", config)
	}
}
