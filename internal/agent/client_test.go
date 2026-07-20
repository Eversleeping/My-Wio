package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCodexEnvironmentIsScopedFromKeyFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "codex.key")
	if err := os.WriteFile(keyFile, []byte("api-key-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	environment := codexEnvironment(Config{CodexAPIKeyFile: keyFile}, log)
	if len(environment) != 1 || environment[0] != "WIO_CODEX_API_KEY=api-key-value" {
		t.Fatalf("unexpected environment: %#v", environment)
	}
	if environment := codexEnvironment(Config{CodexAPIKeyFile: filepath.Join(t.TempDir(), "missing")}, log); environment != nil {
		t.Fatalf("expected no environment, got %#v", environment)
	}
}

func TestRedeliveredOperationReplaysCachedResult(t *testing.T) {
	client := &Client{
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, 2),
		seen:     make(map[string]*operationExecution),
	}
	operation := &protocol.ControlEnvelope{OperationID: "operation-1", Kind: "unsupported"}
	client.handleOperation(context.Background(), operation)
	first := <-client.outbound
	client.handleOperation(context.Background(), operation)
	second := <-client.outbound
	for index, envelope := range []*protocol.AgentEnvelope{first, second} {
		var result protocol.OperationResult
		if envelope.Kind != "operation_result" || json.Unmarshal(envelope.PayloadJSON, &result) != nil || result.OperationID != operation.OperationID || result.Status != "failed" {
			t.Fatalf("unexpected replay %d: %#v payload=%s", index, envelope, envelope.PayloadJSON)
		}
	}
}

func TestSuccessfulConnectionResetsReconnectBackoff(t *testing.T) {
	if backoff := reconnectBackoffAfterResult(32*time.Second, true); backoff != time.Second {
		t.Fatalf("expected successful connection to reset backoff, got %s", backoff)
	}
	if backoff := reconnectBackoffAfterResult(16*time.Second, false); backoff != 16*time.Second {
		t.Fatalf("failed connection should preserve backoff, got %s", backoff)
	}
}

func TestInventoryRootsIncludeCloneRoot(t *testing.T) {
	base := t.TempDir()
	scanRoot := filepath.Join(base, "services")
	cloneRoot := filepath.Join(base, "state", "projects")
	client := &Client{config: Config{ScanRoots: []string{scanRoot, scanRoot}, CloneRoot: cloneRoot}}
	roots := client.inventoryRoots()
	if len(roots) != 2 || roots[0] != scanRoot || roots[1] != cloneRoot {
		t.Fatalf("unexpected inventory roots: %#v", roots)
	}
}

func TestInventoryRootsDoNotDuplicateCoveredCloneRoot(t *testing.T) {
	base := t.TempDir()
	cloneRoot := filepath.Join(base, "state", "projects")
	client := &Client{config: Config{ScanRoots: []string{base}, CloneRoot: cloneRoot}}
	roots := client.inventoryRoots()
	if len(roots) != 1 || roots[0] != base {
		t.Fatalf("unexpected inventory roots: %#v", roots)
	}
}

func TestGitProjectCreateReturnsStructuredResultAndRefreshesInventory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cloneRoot := t.TempDir()
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if output, err := exec.Command("git", "config", "--file", globalConfig, "user.name", "Agent Test").CombinedOutput(); err != nil {
		t.Fatalf("configure test Git name: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "config", "--file", globalConfig, "user.email", "agent@example.com").CombinedOutput(); err != nil {
		t.Fatalf("configure test Git email: %v: %s", err, output)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	client := &Client{
		config:   Config{CloneRoot: cloneRoot},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		outbound: make(chan *protocol.AgentEnvelope, 4),
		seen:     make(map[string]*operationExecution),
	}
	payload, err := json.Marshal(protocol.GitProjectCreateCommand{
		ProjectID:        "project-1",
		WorkspaceID:      "workspace-1",
		Name:             "Sample Service",
		InitialBranch:    "trunk",
		InitializeREADME: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "create-1", Kind: "git.project.create", PayloadJSON: payload})

	operationEnvelope := receiveAgentEnvelope(t, client.outbound)
	if operationEnvelope.Kind != "operation_result" {
		t.Fatalf("expected operation result first, got %q", operationEnvelope.Kind)
	}
	var operation protocol.OperationResult
	if err := json.Unmarshal(operationEnvelope.PayloadJSON, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "succeeded" || operation.OperationID != "create-1" {
		t.Fatalf("unexpected operation result: %#v", operation)
	}
	var result protocol.GitProjectCreateResult
	if err := json.Unmarshal(operation.Data, &result); err != nil {
		t.Fatal(err)
	}
	expectedPath := filepath.Join(cloneRoot, "Sample-Service")
	resultInfo, resultStatErr := os.Stat(result.Path)
	expectedInfo, expectedStatErr := os.Stat(expectedPath)
	if resultStatErr != nil || expectedStatErr != nil || !os.SameFile(resultInfo, expectedInfo) || result.Branch != "trunk" || result.CommitSHA == "" || result.Unborn || result.RemoteURL != "" {
		t.Fatalf("unexpected project result: %#v", result)
	}
	if content, err := os.ReadFile(filepath.Join(result.Path, "README.md")); err != nil || string(content) != "# Sample-Service\n" {
		t.Fatalf("unexpected README: %q %v", content, err)
	}

	inventoryEnvelope := receiveAgentEnvelope(t, client.outbound)
	if inventoryEnvelope.Kind != "inventory" {
		t.Fatalf("expected inventory refresh, got %q", inventoryEnvelope.Kind)
	}
	var inventory protocol.Inventory
	if err := json.Unmarshal(inventoryEnvelope.PayloadJSON, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 || inventory.Repositories[0].Path != result.Path || inventory.Repositories[0].CommitSHA != result.CommitSHA {
		t.Fatalf("created repository missing from inventory: %#v", inventory.Repositories)
	}
}

func TestProjectCreateDestinationRejectsCloneRootEscape(t *testing.T) {
	cloneRoot := t.TempDir()
	outside := filepath.Join(filepath.Dir(cloneRoot), "outside")
	for _, destination := range []string{cloneRoot, outside, ".." + string(filepath.Separator) + "outside"} {
		if _, err := projectCreateDestination(cloneRoot, "service", destination); err == nil {
			t.Fatalf("expected destination %q to be rejected", destination)
		}
	}
	resolved, err := projectCreateDestination(cloneRoot, "Sample Service", "")
	if err != nil || resolved != filepath.Join(cloneRoot, "Sample-Service") {
		t.Fatalf("unexpected default destination: %q %v", resolved, err)
	}
	resolved, err = projectCreateDestination(cloneRoot, "ignored", filepath.Join("team", "service"))
	if err != nil || resolved != filepath.Join(cloneRoot, "team", "service") {
		t.Fatalf("unexpected relative destination: %q %v", resolved, err)
	}
	inside := filepath.Join(cloneRoot, "absolute-service")
	resolved, err = projectCreateDestination(cloneRoot, "ignored", inside)
	if err != nil || resolved != inside {
		t.Fatalf("unexpected absolute destination: %q %v", resolved, err)
	}
}

func TestWorkspaceGitInspectReturnsStatusBranchesRemotesAndCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command("git", "init", "-b", "main", repository).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, output)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	testGitConfig := func(key, value string) {
		if output, err := exec.Command("git", "config", "--file", globalConfig, key, value).CombinedOutput(); err != nil {
			t.Fatalf("configure git: %v: %s", err, output)
		}
	}
	testGitConfig("user.name", "Inspect User")
	testGitConfig("user.email", "inspect@example.com")
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if output, err := exec.Command("git", "-C", repository, "remote", "add", "origin", "https://example.com/team/repository.git").CombinedOutput(); err != nil {
		t.Fatal(string(output))
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("inspect\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "inspect"}} {
		if output, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	client := &Client{config: Config{ScanRoots: []string{root}, CloneRoot: filepath.Join(root, "clone")}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), outbound: make(chan *protocol.AgentEnvelope, 2), seen: make(map[string]*operationExecution)}
	payload, _ := json.Marshal(protocol.GitWorkspaceInspectCommand{WorkspaceID: "workspace-1", Path: repository, CommitLimit: 10})
	client.handleOperation(context.Background(), &protocol.ControlEnvelope{OperationID: "inspect-1", Kind: "git.workspace.inspect", PayloadJSON: payload})
	envelope := receiveAgentEnvelope(t, client.outbound)
	var operation protocol.OperationResult
	if err := json.Unmarshal(envelope.PayloadJSON, &operation); err != nil || operation.Status != "succeeded" {
		t.Fatalf("unexpected inspect operation: %#v %v", operation, err)
	}
	var result protocol.GitWorkspaceInspectResult
	if err := json.Unmarshal(operation.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != "workspace-1" || result.Status.Branch != "main" || result.Status.Head == "" || len(result.Branches) == 0 || len(result.Remotes) != 1 || len(result.Commits) != 1 || result.Commits[0].Title != "inspect" {
		t.Fatalf("unexpected inspect result: %#v", result)
	}
}

func receiveAgentEnvelope(t *testing.T, outbound <-chan *protocol.AgentEnvelope) *protocol.AgentEnvelope {
	t.Helper()
	select {
	case envelope := <-outbound:
		return envelope
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Agent envelope")
		return nil
	}
}
