package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestStageAgentUpdateDownloadsAndVerifiesPackage(t *testing.T) {
	packageBody := []byte("new-agent-binary")
	hash := sha256.Sum256(packageBody)
	expectedSHA := hex.EncodeToString(hash[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" {
			t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write(packageBody)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	client := &Client{config: Config{ControlURL: server.URL, AgentToken: "agent-token", StateDir: stateDir}}
	command := protocol.AgentUpdateCommand{Version: "0.3.0", Packages: map[string]protocol.AgentUpdatePackage{
		runtime.GOARCH: {URL: "/api/agent/update-package/" + runtime.GOARCH + "?version=0.3.0", SHA256: expectedSHA, Size: int64(len(packageBody))},
	}}
	path, err := client.stageAgentUpdate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !matchesFileHash(path, expectedSHA) {
		t.Fatal("staged Agent binary did not match the package checksum")
	}
	manifest, resolved, err := readAgentUpdateManifest(filepath.Join(stateDir, "updates"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != command.Version || manifest.State != "pending" || !samePath(path, resolved) {
		t.Fatalf("unexpected update manifest: %#v path=%q", manifest, resolved)
	}
}

func TestStageAgentUpdateRejectsChecksumMismatch(t *testing.T) {
	packageBody := []byte("tampered-binary")
	expected := sha256.Sum256([]byte("expected-binary"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(packageBody) }))
	defer server.Close()

	stateDir := t.TempDir()
	client := &Client{config: Config{ControlURL: server.URL, AgentToken: "agent-token", StateDir: stateDir}}
	_, err := client.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{Version: "0.3.0", Packages: map[string]protocol.AgentUpdatePackage{
		runtime.GOARCH: {URL: "/api/agent/update-package/" + runtime.GOARCH, SHA256: hex.EncodeToString(expected[:]), Size: int64(len(packageBody))},
	}})
	if err == nil {
		t.Fatal("expected a checksum mismatch")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "updates", "current.json")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid package created a current manifest: %v", statErr)
	}
}

func TestAgentUpdateURLRejectsAnotherHost(t *testing.T) {
	if _, err := agentUpdateURL("https://control.example.com", "https://attacker.example.com/api/agent/update-package/amd64"); err == nil {
		t.Fatal("expected a cross-host package URL to be rejected")
	}
}

func TestActivateCurrentUpdateRollsBackAnUnconfirmedAttempt(t *testing.T) {
	stateDir := t.TempDir()
	updatesRoot := filepath.Join(stateDir, "updates")
	packageBody := []byte("failed-agent-binary")
	hash := sha256.Sum256(packageBody)
	checksum := hex.EncodeToString(hash[:])
	binaryDir := filepath.Join(updatesRoot, checksum)
	if err := os.MkdirAll(binaryDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binaryDir, "wio-agent"), packageBody, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentUpdateManifest(updatesRoot, agentUpdateManifest{Version: "0.3.0", Binary: filepath.ToSlash(filepath.Join(checksum, "wio-agent")), SHA256: checksum, State: "attempting"}); err != nil {
		t.Fatal(err)
	}
	if err := ActivateCurrentUpdate(stateDir); err == nil {
		t.Fatal("expected the unconfirmed Agent update to be rolled back")
	}
	if _, err := os.Stat(filepath.Join(updatesRoot, "current.json")); !os.IsNotExist(err) {
		t.Fatalf("rollback left the update manifest active: %v", err)
	}
}
