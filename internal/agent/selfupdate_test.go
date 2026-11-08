package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	command := protocol.AgentUpdateCommand{Version: "1.0.0", Packages: map[string]protocol.AgentUpdatePackage{
		runtime.GOARCH: {URL: "/api/agent/update-package/" + runtime.GOARCH + "?version=1.0.0", SHA256: expectedSHA, Size: int64(len(packageBody))},
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
	// The reservation must survive staging until exec; a rejected duplicate
	// updater must not clear it in the result-delivery/restart window.
	if !client.agentUpdatePending() {
		t.Fatal("update reservation released before activation")
	}
	if _, err := client.stageAgentUpdate(context.Background(), command); err == nil || !client.agentUpdatePending() {
		t.Fatal("duplicate updater cleared the active reservation")
	}
	for _, kind := range []string{"gemini.turn.start", "gemini.turn.rewrite", "codex.turn.start", "codex.turn.rewrite"} {
		payload, _ := json.Marshal(protocol.GeminiTurnCommand{})
		err := client.execute(context.Background(), &protocol.ControlEnvelope{Kind: kind, PayloadJSON: payload})
		if err == nil || !strings.Contains(err.Error(), "update is in progress") {
			t.Fatalf("%s allowed during pending update: %v", kind, err)
		}
	}
}

func TestStageAgentUpdateRejectsChecksumMismatch(t *testing.T) {
	packageBody := []byte("tampered-binary")
	expected := sha256.Sum256([]byte("expected-binary"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(packageBody) }))
	defer server.Close()

	stateDir := t.TempDir()
	client := &Client{config: Config{ControlURL: server.URL, AgentToken: "agent-token", StateDir: stateDir}}
	_, err := client.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{Version: "1.0.0", Packages: map[string]protocol.AgentUpdatePackage{
		runtime.GOARCH: {URL: "/api/agent/update-package/" + runtime.GOARCH, SHA256: hex.EncodeToString(expected[:]), Size: int64(len(packageBody))},
	}})
	if err == nil {
		t.Fatal("expected a checksum mismatch")
	}
	if client.agentUpdatePending() {
		t.Fatal("failed update left the Agent blocked")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "updates", "current.json")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid package created a current manifest: %v", statErr)
	}
}

func TestStageAgentUpdateRejectsActiveGeminiExecution(t *testing.T) {
	for _, kind := range []string{"gemini.turn.start", "gemini.turn.rewrite", "gemini.thread.fork", "git.worktree.create"} {
		t.Run(kind, func(t *testing.T) {
			done := make(chan struct{})
			client := &Client{seen: map[string]*operationExecution{"running": {kind: kind, done: done}}}
			if _, err := client.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{}); err == nil || !strings.Contains(err.Error(), "active Gemini") {
				t.Fatalf("active execution did not prevent update: %v", err)
			}
			if client.agentUpdatePending() {
				t.Fatal("rejected update reserved the Agent")
			}
			close(done)
			if _, err := client.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{}); err == nil || strings.Contains(err.Error(), "active Gemini") {
				t.Fatalf("completed execution still prevents update validation: %v", err)
			}
		})
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
	if err := writeAgentUpdateManifest(updatesRoot, agentUpdateManifest{Version: "1.0.0", Binary: filepath.ToSlash(filepath.Join(checksum, "wio-agent")), SHA256: checksum, State: "attempting"}); err != nil {
		t.Fatal(err)
	}
	if err := ActivateCurrentUpdate(stateDir); err == nil {
		t.Fatal("expected the unconfirmed Agent update to be rolled back")
	}
	if _, err := os.Stat(filepath.Join(updatesRoot, "current.json")); !os.IsNotExist(err) {
		t.Fatalf("rollback left the update manifest active: %v", err)
	}
}

func TestStageAgentUpdateRejectsActiveNonIdempotentOperations(t *testing.T) {
	for _, kind := range []string{"deploy.start", "deploy.rollback", "git.workspace.write", "git.workspace.lifecycle", "git.workspace.clone", "git.project.create", "workspace.file.write", "cli.install", "codex.update", "credentials.configure"} {
		t.Run(kind, func(t *testing.T) {
			client := &Client{seen: map[string]*operationExecution{"running": {kind: kind, done: make(chan struct{})}}}
			if _, err := client.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{}); err == nil || !strings.Contains(err.Error(), "finish active operations") || !strings.Contains(err.Error(), kind) {
				t.Fatalf("active %s did not prevent update: %v", kind, err)
			}
			if client.agentUpdatePending() {
				t.Fatal("rejected update reserved the Agent")
			}
		})
	}
}

func TestStageAgentUpdateAllowsRestartSafeOperations(t *testing.T) {
	seen := map[string]*operationExecution{"self": {kind: "agent.update", done: make(chan struct{})}}
	for _, kind := range []string{"workspace.files", "workspace.changes", "git.workspace.inspect", "inventory.scan", "operation.interrupt"} {
		seen[kind] = &operationExecution{kind: kind, done: make(chan struct{})}
	}
	client := &Client{seen: seen}
	// Validation of the empty command fails only after the blocker scan passed.
	if _, err := client.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{}); err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("restart-safe operations blocked the update: %v", err)
	}
}

func TestPendingUpdateRefusesNewNonIdempotentOperations(t *testing.T) {
	client := &Client{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, 8),
		seen:     make(map[string]*operationExecution),
	}
	client.updatePending = true
	for _, kind := range []string{"deploy.start", "git.workspace.write", "workspace.file.write"} {
		client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: kind, Kind: kind, PayloadJSON: json.RawMessage(`{}`)})
		receiveOperationStarted(t, client.outbound, kind)
		var result protocol.OperationResult
		envelope := receiveAgentEnvelope(t, client.outbound)
		if json.Unmarshal(envelope.PayloadJSON, &result) != nil || result.Status != "failed" || !strings.Contains(result.Message, "update is in progress") {
			t.Fatalf("%s ran during a pending update: %#v", kind, result)
		}
	}
	client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "scan", Kind: "workspace.files", PayloadJSON: json.RawMessage(`{"path":"/nonexistent"}`)})
	receiveOperationStarted(t, client.outbound, "scan")
	var result protocol.OperationResult
	envelope := receiveAgentEnvelope(t, client.outbound)
	if json.Unmarshal(envelope.PayloadJSON, &result) != nil || strings.Contains(result.Message, "update is in progress") {
		t.Fatalf("restart-safe read was refused during a pending update: %#v", result)
	}
}
