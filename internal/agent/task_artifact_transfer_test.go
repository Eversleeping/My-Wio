package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/taskartifact"
)

func TestTaskArtifactTransferBetweenAgentRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real Git fixtures")
	}
	ctx := context.Background()
	source, parent := t.TempDir(), t.TempDir()
	dest := filepath.Join(parent, "other-checkout")
	target := filepath.Join(parent, "review")
	git := func(directory string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Git fixture: %s: %v", out, err)
		}
	}
	git(source, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", "base.txt")
	git(source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "base")
	git(parent, "clone", "--no-local", source, dest)
	git(dest, "worktree", "add", "-b", "review", target, "HEAD")
	if err := os.WriteFile(filepath.Join(source, "code.bin"), []byte{0, 2, 9}, 0600); err != nil {
		t.Fatal(err)
	}
	a, b := outboundTestClient(1), outboundTestClient(1)
	a.config.ScanRoots = []string{source}
	b.config.ScanRoots = []string{parent}
	capture, _ := json.Marshal(protocol.TaskArtifactCapture{Workspace: source, Paths: []string{"code.bin"}})
	_, raw, _, err := a.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: protocol.OpTaskArtifactCapture, PayloadJSON: capture})
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.ArtifactManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	hash, err := taskartifact.Validate(m)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(protocol.TaskArtifactHandoff{Workspace: target, Manifest: m, Digest: hash, Action: "apply"})
	if _, _, _, err := a.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: protocol.OpTaskArtifactHandoff, PayloadJSON: input}); err == nil {
		t.Fatal("source Agent accepted a foreign root")
	}
	_, receipt, _, err := b.dispatchOperation(ctx, &protocol.ControlEnvelope{Kind: protocol.OpTaskArtifactHandoff, PayloadJSON: input})
	if err != nil {
		t.Fatal(err)
	}
	var confirmed protocol.TaskArtifactReceipt
	if err := json.Unmarshal(receipt, &confirmed); err != nil || confirmed.Digest != hash {
		t.Fatal(confirmed, err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "code.bin")); err != nil || taskartifact.Hash(got) != m.Files[0].AfterHash {
		t.Fatal("target content mismatch", err)
	}
}
