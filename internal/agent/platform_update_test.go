package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/wio-platform/wio/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceUpdateRecoversVerifiedPreviousPackage(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "updates")
	body := []byte("previous verified Agent")
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	previous := filepath.Join(hash, agentExecutableName())
	if err := os.MkdirAll(filepath.Join(root, hash), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, previous), body, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := agentUpdateManifest{Version: "99.0.0", Binary: "bad/" + agentExecutableName(), SHA256: "bad", State: "attempting", PreviousBinary: filepath.ToSlash(previous), PreviousVersion: "0.3.47", PreviousSHA256: hash}
	if err := writeAgentUpdateManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	binary, err := ServiceUpdateBinary(state, "base")
	if err != nil || binary != filepath.Join(root, previous) {
		t.Fatalf("recovery=%s %v", binary, err)
	}
	restored, _, err := readAgentUpdateManifest(root)
	if err != nil || restored.State != "healthy" || !restored.AllowRollback {
		t.Fatalf("manifest=%+v %v", restored, err)
	}
	if err := os.WriteFile(filepath.Join(root, previous), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err = ServiceUpdateBinary(state, "base")
	if binary != "base" || err == nil {
		t.Fatalf("tampered previous accepted: %s %v", binary, err)
	}
}
func TestRollbackRespectsActiveOperations(t *testing.T) {
	c := &Client{config: Config{StateDir: t.TempDir()}, seen: map[string]*operationExecution{"write": {kind: protocol.OpWorkspaceFileWrite, done: make(chan struct{})}}}
	if _, err := c.stageAgentUpdate(context.Background(), protocol.AgentUpdateCommand{Rollback: true}); err == nil {
		t.Fatal("rollback interrupted an active write")
	}
	if c.agentUpdatePending() {
		t.Fatal("failed rollback left reservation")
	}
}
