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
	"time"

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
	previousBinary := strings.Repeat("b", 64) + "/wio-agent"
	if err := writeAgentUpdateManifest(filepath.Join(stateDir, "updates"), agentUpdateManifest{Version: "0.1.0", Binary: previousBinary, SHA256: strings.Repeat("b", 64), State: "healthy"}); err != nil {
		t.Fatal(err)
	}
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
	if manifest.Version != command.Version || manifest.State != "pending" || manifest.PreviousBinary != previousBinary || !samePath(path, resolved) {
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

func TestHealthyAgentUpdateDoesNotBecomeAnUnconfirmedAttempt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "updates")
	body := []byte("invalid executable used to exercise failed exec without replacing the test process")
	hash := sha256.Sum256(body)
	checksum := hex.EncodeToString(hash[:])
	dir := filepath.Join(root, checksum)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wio-agent"), body, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := agentUpdateManifest{Version: "1.0.0", Binary: checksum + "/wio-agent", SHA256: checksum, State: "healthy"}
	if err := writeAgentUpdateManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ActivateCurrentUpdate(filepath.Dir(root)); err == nil {
			t.Fatal("invalid executable unexpectedly activated")
		}
		current, _, err := readAgentUpdateManifest(root)
		if err != nil || current.State != "healthy" {
			t.Fatalf("confirmed update lost healthy state across restart: %+v %v", current, err)
		}
	}
}

func TestPruneAgentUpdatesKeepsCurrentAndPreviousPackages(t *testing.T) {
	root := t.TempDir()
	current, previous, obsolete := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, name := range []string{current, previous, obsolete, "unmanaged"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "wio-agent"), []byte(name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	link := filepath.Join(root, strings.Repeat("d", 64))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	manifest := agentUpdateManifest{Binary: current + "/wio-agent", PreviousBinary: previous + "/wio-agent"}
	if err := pruneAgentUpdates(root, manifest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{current, previous, "unmanaged"} {
		if _, err := os.Stat(filepath.Join(root, name, "wio-agent")); err != nil {
			t.Fatalf("retained package removed: %s %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, obsolete)); !os.IsNotExist(err) {
		t.Fatalf("obsolete package retained: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("cleanup followed or removed a symlink: %v", err)
	}
}

func TestPruneLegacyAgentUpdatesRetainsRecoveryPackage(t *testing.T) {
	root := t.TempDir()
	current, previous, obsolete := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	now := time.Now()
	for i, name := range []string{current, previous, obsolete} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "wio-agent")
		if err := os.WriteFile(path, []byte(name), 0o750); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneAgentUpdates(root, agentUpdateManifest{Binary: current + "/wio-agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, previous, "wio-agent")); err != nil {
		t.Fatalf("legacy cleanup lost recovery package: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, obsolete)); !os.IsNotExist(err) {
		t.Fatalf("legacy cleanup retained obsolete package: %v", err)
	}
}
