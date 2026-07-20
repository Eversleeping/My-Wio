package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCrossServerCloneOperationReturnsVerifiedWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	remoteRoot := t.TempDir()
	remote := filepath.Join(remoteRoot, "project.git")
	work := filepath.Join(t.TempDir(), "work")
	for _, command := range [][]string{{"init", "--bare", remote}, {"init", "-b", "main", work}} {
		if output, err := exec.Command("git", command...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", command, err, output)
		}
	}
	for _, command := range [][]string{{"config", "user.name", "Clone Test"}, {"config", "user.email", "clone@example.com"}} {
		if output, err := exec.Command("git", append([]string{"-C", work}, command...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", command, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# Clone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"add", "README.md"}, {"commit", "-m", "initial"}, {"remote", "add", "origin", remote}, {"push", "origin", "main"}} {
		if output, err := exec.Command("git", append([]string{"-C", work}, command...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", command, err, output)
		}
	}
	if output, err := exec.Command("git", "-C", remote, "update-server-info").CombinedOutput(); err != nil {
		t.Fatalf("update server info: %v: %s", err, output)
	}
	headOutput, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := string(bytes.TrimSpace(headOutput))
	server := httptest.NewTLSServer(http.FileServer(http.Dir(remoteRoot)))
	t.Cleanup(server.Close)
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	cloneRoot := t.TempDir()
	target := filepath.Join(cloneRoot, "target")
	client := &Client{config: Config{CloneRoot: cloneRoot}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), outbound: make(chan *protocol.AgentEnvelope, 2), seen: make(map[string]*operationExecution)}
	command := protocol.GitWorkspaceCloneCommand{WorkspaceID: "target-workspace", ProjectID: "project-1", Name: "project", Destination: target, RemoteURL: server.URL + "/project.git", Branch: "main", ExpectedHead: head}
	payload, _ := json.Marshal(command)
	client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "clone-1", Kind: "git.workspace.clone", PayloadJSON: payload})
	envelope := receiveAgentEnvelope(t, client.outbound)
	var operation protocol.OperationResult
	if err := json.Unmarshal(envelope.PayloadJSON, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "succeeded" {
		t.Fatalf("clone failed: %#v", operation)
	}
	var result protocol.GitWorkspaceCloneResult
	if err := json.Unmarshal(operation.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != command.WorkspaceID || result.CommitSHA != head || result.Path != target {
		t.Fatalf("unexpected clone result: %#v", result)
	}
}

func TestWorkspaceLifecycleOperationReturnsStructuredMoveResult(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cloneRoot := t.TempDir()
	source := filepath.Join(cloneRoot, "source")
	target := filepath.Join(cloneRoot, "target")
	if output, err := exec.Command("git", "init", "-b", "main", source).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, output)
	}
	client := &Client{config: Config{CloneRoot: cloneRoot}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), outbound: make(chan *protocol.AgentEnvelope, 2), seen: make(map[string]*operationExecution)}
	command := protocol.GitWorkspaceLifecycleCommand{WorkspaceID: "workspace-1", ProjectID: "project-1", Action: "move", SourcePath: source, TargetPath: target}
	payload, _ := json.Marshal(command)
	client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "move-1", Kind: "git.workspace.lifecycle", PayloadJSON: payload})
	envelope := receiveAgentEnvelope(t, client.outbound)
	var operation protocol.OperationResult
	if err := json.Unmarshal(envelope.PayloadJSON, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "succeeded" {
		t.Fatalf("unexpected lifecycle failure: %#v", operation)
	}
	var result protocol.GitWorkspaceLifecycleResult
	if err := json.Unmarshal(operation.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != command.WorkspaceID || result.Action != "move" || result.TargetPath != target {
		t.Fatalf("unexpected lifecycle result: %#v", result)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target does not exist: %v", err)
	}
}
