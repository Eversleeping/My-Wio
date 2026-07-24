package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/agentgateway"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
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

func TestQueueDefaultControlPlaneCredentials(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	vault := security.DevVault()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := database.EnsureControlPlaneServer(ctx, "control-host", "control-token"); err != nil {
		t.Fatal(err)
	}
	codexSecret, _ := vault.Encrypt("codex-secret-value")
	codex, err := database.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "codex", Name: "Codex", Endpoint: "https://api.example.com/v1", Model: "gpt-5.6-sol"}, codexSecret)
	if err != nil {
		t.Fatal(err)
	}
	gitSecret, _ := vault.Encrypt("git-token-value")
	git, err := database.SaveCredentialProfile(ctx, store.CredentialProfile{Kind: "git", Name: "Git", Endpoint: "https://gitee.com", Username: "wio", CommitName: "Wio", CommitEmail: "wio@example.com"}, gitSecret)
	if err != nil {
		t.Fatal(err)
	}
	gateway := agentgateway.New(database, realtime.New(), vault, log)
	if err := queueDefaultControlPlaneCredentials(ctx, database, vault, gateway, log); err != nil {
		t.Fatal(err)
	}
	ops, err := database.PendingOperations(ctx, store.ControlPlaneServerID)
	if err != nil || len(ops) != 1 || ops[0].Kind != "credentials.configure" {
		t.Fatalf("unexpected credential operation: %#v %v", ops, err)
	}
	var command protocol.ConfigureCredentialsCommand
	if err := vault.Decrypt(ops[0].Payload, &command); err != nil {
		t.Fatal(err)
	}
	if command.CodexAPIKey != "codex-secret-value" || command.GitToken != "git-token-value" || command.GitCommitEmail != git.CommitEmail {
		t.Fatalf("unexpected default credential command: %#v", command)
	}
	if err := database.SetServerCredentialProfiles(ctx, store.ControlPlaneServerID, codex.ID, git.ID); err != nil {
		t.Fatal(err)
	}
	if err := queueDefaultControlPlaneCredentials(ctx, database, vault, gateway, log); err != nil {
		t.Fatal(err)
	}
	ops, err = database.PendingOperations(ctx, store.ControlPlaneServerID)
	if err != nil || len(ops) != 1 {
		t.Fatalf("explicit binding should prevent another default operation: %#v %v", ops, err)
	}
}
