package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/codexadapter"
	"github.com/wio-platform/wio/internal/deployer"
	"github.com/wio-platform/wio/internal/gitrepository"
	"github.com/wio-platform/wio/internal/gitworktree"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

const initialReconnectBackoff = time.Second

type Client struct {
	config      Config
	log         *slog.Logger
	outbound    chan *protocol.AgentEnvelope
	codex       *codexadapter.Adapter
	deployer    *deployer.Deployer
	seenMu      sync.Mutex
	seen        map[string]*operationExecution
	codexPathMu sync.RWMutex
	codexPath   string
}

type operationExecution struct {
	done   chan struct{}
	result protocol.OperationResult
}

func NewClient(config Config, log *slog.Logger) *Client {
	codexPath := effectiveCodexPath(config)
	client := &Client{config: config, log: log, outbound: make(chan *protocol.AgentEnvelope, 4096), deployer: deployer.New(config.DockerPath), seen: make(map[string]*operationExecution), codexPath: codexPath}
	client.codex = codexadapter.NewWithEnvironment(codexPath, codexEnvironment(config, log), log, func(event protocol.StreamEvent) error {
		if event.EventID == "" {
			event.EventID = uuid.NewString()
		}
		return client.queue("event", event, true)
	})
	return client
}

func codexEnvironment(config Config, log *slog.Logger) []string {
	raw, err := os.ReadFile(config.CodexAPIKeyFile)
	if err != nil {
		log.Warn("Codex API key is unavailable", "path", config.CodexAPIKeyFile, "error", err)
		return nil
	}
	key := strings.TrimSpace(string(raw))
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		log.Warn("Codex API key file is invalid", "path", config.CodexAPIKeyFile)
		return nil
	}
	return []string{"WIO_CODEX_API_KEY=" + key}
}

func (c *Client) Run(ctx context.Context) error {
	defer c.codex.Close()
	backoff := initialReconnectBackoff
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		connected, err := c.connect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		backoff = reconnectBackoffAfterResult(backoff, connected)
		c.log.Warn("agent connection ended", "error", err, "retry_in", backoff)
		jitter := time.Duration(rand.Int63n(int64(backoff/2 + 1)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + jitter):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func reconnectBackoffAfterResult(current time.Duration, connected bool) time.Duration {
	if connected {
		return initialReconnectBackoff
	}
	return current
}

func (c *Client) connect(ctx context.Context) (bool, error) {
	parsed, err := url.Parse(c.config.ControlURL)
	if err != nil {
		return false, err
	}
	var transport credentials.TransportCredentials
	if parsed.Scheme == "https" {
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname(), InsecureSkipVerify: c.config.InsecureSkipVerify})
	} else {
		transport = insecure.NewCredentials()
	}
	connection, err := grpc.NewClient(parsed.Host, grpc.WithTransportCredentials(transport), grpc.WithDefaultCallOptions(grpc.ForceCodec(protocol.Codec()), grpc.MaxCallRecvMsgSize(8<<20), grpc.MaxCallSendMsgSize(8<<20)))
	if err != nil {
		return false, err
	}
	defer connection.Close()
	streamContext, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.config.AgentToken))
	defer cancel()
	stream, err := protocol.NewAgentServiceClient(connection).Connect(streamContext)
	if err != nil {
		return false, err
	}
	_ = c.enqueueHeartbeat(ctx)
	_ = c.enqueueMetrics(ctx)
	_ = c.enqueueInventory(ctx)
	sendErrors := make(chan error, 1)
	go c.sendLoop(streamContext, stream, sendErrors)
	go c.periodic(streamContext)
	connected := false
	for {
		command, err := stream.Recv()
		if err != nil {
			cancel()
			return connected, err
		}
		if !connected {
			connected = true
			confirmed, confirmErr := confirmCurrentUpdate(c.config.StateDir)
			if confirmErr != nil {
				c.log.Warn("could not confirm Agent update", "error", confirmErr)
			} else if confirmed {
				_ = c.queue("agent_update_status", map[string]string{"version": buildinfo.Version, "status": "healthy"}, true)
			}
			c.log.Info("connected to Wio control plane", "server_id", c.config.ServerID)
		}
		select {
		case err := <-sendErrors:
			cancel()
			return connected, err
		default:
		}
		if command.Kind == protocol.ControlKindKeepalive && command.OperationID == "" {
			continue
		}
		go c.handleOperation(ctx, command)
	}
}

func (c *Client) sendLoop(ctx context.Context, stream protocol.AgentServiceConnectClient, errors chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case envelope := <-c.outbound:
			if err := stream.Send(envelope); err != nil {
				select {
				case c.outbound <- envelope:
				default:
				}
				select {
				case errors <- err:
				default:
				}
				return
			}
		}
	}
}

func (c *Client) periodic(ctx context.Context) {
	heartbeats := time.NewTicker(15 * time.Second)
	metrics := time.NewTicker(15 * time.Second)
	inventory := time.NewTicker(2 * time.Minute)
	defer heartbeats.Stop()
	defer metrics.Stop()
	defer inventory.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeats.C:
			_ = c.enqueueHeartbeat(ctx)
		case <-metrics.C:
			_ = c.enqueueMetrics(ctx)
		case <-inventory.C:
			_ = c.enqueueInventory(ctx)
		}
	}
}

func (c *Client) handleOperation(parent context.Context, envelope *protocol.ControlEnvelope) {
	if envelope.OperationID == "" {
		return
	}
	c.seenMu.Lock()
	execution, seen := c.seen[envelope.OperationID]
	if seen {
		done := execution.done
		c.seenMu.Unlock()
		select {
		case <-done:
			c.seenMu.Lock()
			result := execution.result
			c.seenMu.Unlock()
			_ = c.queue("operation_result", result, true)
		case <-parent.Done():
		}
		return
	}
	execution = &operationExecution{done: make(chan struct{})}
	c.seen[envelope.OperationID] = execution
	c.seenMu.Unlock()
	timeout := 10 * time.Minute
	if strings.HasPrefix(envelope.Kind, "deploy.") {
		timeout = time.Hour
	} else if envelope.Kind == "agent.update" {
		timeout = 30 * time.Minute
	} else if envelope.Kind == "codex.update" {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var restartPath string
	var resultData json.RawMessage
	refreshInventory := false
	var err error
	if envelope.Kind == "agent.update" {
		var command protocol.AgentUpdateCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			restartPath, err = c.stageAgentUpdate(ctx, command)
		}
	} else if envelope.Kind == "workspace.files" {
		var command protocol.WorkspaceFilesCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.WorkspaceFilesResult
			result, err = scanner.ListWorkspaceFiles(ctx, command.Path, c.inventoryRoots(), 4000)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == "workspace.file.preview" {
		var command protocol.WorkspaceFilePreviewCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.WorkspaceFilePreviewResult
			result, err = scanner.ReadWorkspaceFile(ctx, command.Root, command.Path, c.inventoryRoots(), 1024*1024)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if strings.HasPrefix(envelope.Kind, "codex.goal.") || envelope.Kind == "codex.mcp.list" || envelope.Kind == "codex.skills.list" || envelope.Kind == "codex.status.snapshot" {
		var result protocol.CodexCapabilityResult
		result, err = c.codex.CodexOperation(ctx, envelope.Kind, envelope.PayloadJSON)
		if err == nil {
			resultData, err = json.Marshal(result)
		}
	} else if envelope.Kind == "codex.thread.fork" {
		var command protocol.ForkThreadCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.ForkThreadResult
			result, err = c.codex.ForkThread(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == "git.project.create" {
		var command protocol.GitProjectCreateCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitProjectCreateResult
			result, err = c.createProject(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
				refreshInventory = err == nil
			}
		}
	} else if envelope.Kind == "git.project.delete" {
		var command protocol.GitProjectDeleteCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitProjectDeleteResult
			result, err = c.deleteProject(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
				refreshInventory = err == nil
			}
		}
	} else if envelope.Kind == "git.workspace.inspect" {
		var command protocol.GitWorkspaceInspectCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorkspaceInspectResult
			result, err = c.inspectWorkspaceGit(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == "git.workspace.write" {
		var command protocol.GitWorkspaceWriteCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorkspaceWriteResult
			result, err = c.writeWorkspaceGit(ctx, command)
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == "git.workspace.lifecycle" {
		var command protocol.GitWorkspaceLifecycleCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorkspaceLifecycleResult
			result.WorkspaceID, result.TargetWorkspaceID, result.Action, result.SourcePath = command.WorkspaceID, command.TargetWorkspaceID, command.Action, command.SourcePath
			switch command.Action {
			case "move":
				_, err = gitrepository.MoveWorkspace(ctx, command.SourcePath, command.TargetPath, c.managedRoots())
			case "copy":
				_, err = gitrepository.CopyWorkspace(ctx, command.SourcePath, command.TargetPath, c.managedRoots())
			case "delete":
				err = gitrepository.DeleteWorkspace(ctx, command.SourcePath, command.Force, c.managedRoots())
			default:
				err = fmt.Errorf("unsupported workspace lifecycle action %q", command.Action)
			}
			if err == nil {
				if command.Action != "delete" {
					result.TargetPath = filepath.Clean(command.TargetPath)
				}
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == "git.workspace.clone" {
		var command protocol.GitWorkspaceCloneCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			destination, destinationErr := projectCreateDestination(c.config.CloneRoot, command.Name, command.Destination)
			if destinationErr != nil {
				err = destinationErr
			} else {
				var cloned gitrepository.CloneWorkspaceResult
				cloned, err = gitrepository.CloneWorkspace(ctx, gitrepository.CloneWorkspaceOptions{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: destination, RemoteURL: command.RemoteURL, Branch: command.Branch, ExpectedHead: command.ExpectedHead}, c.managedRoots())
				if err == nil {
					resultData, err = json.Marshal(protocol.GitWorkspaceCloneResult{WorkspaceID: command.WorkspaceID, ProjectID: command.ProjectID, Path: filepath.Clean(command.Destination), Branch: cloned.Branch, CommitSHA: cloned.CommitSHA})
				}
			}
		}
	} else if envelope.Kind == "git.worktree.create" {
		var command protocol.GitWorktreeCreateCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			var result protocol.GitWorktreeCreateResult
			result, err = gitworktree.Create(ctx, command, c.inventoryRoots())
			if err == nil && command.TargetThreadID != "" {
				forkCommand := protocol.ForkThreadCommand{SourceThreadID: command.SourceThreadID, TargetThreadID: command.TargetThreadID, CodexThread: command.CodexThread, WorkspaceID: command.TargetWorkspaceID, Workspace: result.Path, Title: command.Title}
				var fork protocol.ForkThreadResult
				fork, err = c.codex.ForkThread(ctx, forkCommand)
				if err != nil {
					cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
					cleanupErr := gitworktree.Remove(cleanupContext, command.SourcePath, result.Path, command.Branch, c.inventoryRoots())
					cancelCleanup()
					if cleanupErr != nil {
						err = fmt.Errorf("fork Codex thread: %v; cleanup worktree: %w", err, cleanupErr)
					}
				} else {
					result.CodexThread = fork.CodexThread
				}
			}
			if err == nil {
				resultData, err = json.Marshal(result)
			}
		}
	} else if envelope.Kind == "git.worktree.cleanup" {
		var command protocol.GitWorktreeCleanupCommand
		if err = json.Unmarshal(envelope.PayloadJSON, &command); err == nil {
			err = gitworktree.Remove(ctx, command.SourcePath, command.TargetPath, command.Branch, c.inventoryRoots())
		}
	} else {
		err = c.execute(ctx, envelope)
	}
	status := "succeeded"
	message := ""
	if err != nil {
		status = "failed"
		message = err.Error()
		c.log.Warn("operation failed", "operation_id", envelope.OperationID, "kind", envelope.Kind, "error", err)
	}
	result := protocol.OperationResult{OperationID: envelope.OperationID, Status: status, Message: truncate(message, 8192), Data: resultData}
	c.seenMu.Lock()
	execution.result = result
	close(execution.done)
	c.seenMu.Unlock()
	if queueErr := c.queue("operation_result", result, true); queueErr != nil {
		c.log.Warn("could not queue operation result", "operation_id", envelope.OperationID, "error", queueErr)
	}
	if refreshInventory {
		go c.refreshInventory()
	}
	if err == nil && restartPath != "" {
		time.Sleep(750 * time.Millisecond)
		if restartErr := activateStagedUpdate(c.config.StateDir, restartPath); restartErr != nil {
			c.log.Error("could not restart into Agent update", "operation_id", envelope.OperationID, "error", restartErr)
			failure := protocol.OperationResult{OperationID: envelope.OperationID, Status: "failed", Message: truncate(restartErr.Error(), 8192)}
			c.seenMu.Lock()
			execution.result = failure
			c.seenMu.Unlock()
			_ = c.queue("operation_result", failure, true)
		}
	}
}

func (c *Client) inspectWorkspaceGit(ctx context.Context, command protocol.GitWorkspaceInspectCommand) (protocol.GitWorkspaceInspectResult, error) {
	if strings.TrimSpace(command.WorkspaceID) == "" {
		return protocol.GitWorkspaceInspectResult{}, errors.New("workspace ID is required")
	}
	status, err := gitrepository.GetStatus(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	branches, err := gitrepository.ListBranches(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	remotes, err := gitrepository.ListRemotes(ctx, command.Path, c.inventoryRoots())
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	limit := command.CommitLimit
	if limit <= 0 {
		limit = 50
	}
	commits, err := gitrepository.ListCommits(ctx, command.Path, c.inventoryRoots(), gitrepository.CommitLogOptions{Limit: limit})
	if err != nil {
		return protocol.GitWorkspaceInspectResult{}, err
	}
	result := protocol.GitWorkspaceInspectResult{WorkspaceID: command.WorkspaceID, Status: protocol.GitStatus{Branch: status.Branch, Detached: status.Detached, Unborn: status.Unborn, Head: status.Head, Upstream: status.Upstream, Ahead: status.Ahead, Behind: status.Behind, Staged: status.Staged, Unstaged: status.Unstaged, Untracked: status.Untracked, Dirty: status.Dirty}, HasMore: commits.HasMore}
	result.Branches = make([]protocol.GitBranch, 0, len(branches))
	for _, branch := range branches {
		result.Branches = append(result.Branches, protocol.GitBranch{Name: branch.Name, FullName: branch.FullName, Kind: branch.Kind, CommitSHA: branch.CommitSHA, Upstream: branch.Upstream, Current: branch.Current})
	}
	result.Remotes = make([]protocol.GitRemote, 0, len(remotes))
	for _, remote := range remotes {
		result.Remotes = append(result.Remotes, protocol.GitRemote{Name: remote.Name, FetchURLs: remote.FetchURLs, PushURLs: remote.PushURLs})
	}
	result.Commits = make([]protocol.GitCommit, 0, len(commits.Commits))
	for _, commit := range commits.Commits {
		result.Commits = append(result.Commits, protocol.GitCommit{SHA: commit.SHA, AuthorName: commit.AuthorName, AuthorEmail: commit.AuthorEmail, AuthoredAt: commit.AuthoredAt, Title: commit.Title, Parents: commit.Parents})
	}
	return result, nil
}

func (c *Client) writeWorkspaceGit(ctx context.Context, command protocol.GitWorkspaceWriteCommand) (protocol.GitWorkspaceWriteResult, error) {
	if command.WorkspaceID == "" || command.Path == "" || command.Action == "" {
		return protocol.GitWorkspaceWriteResult{}, errors.New("workspace Git write command is incomplete")
	}
	roots := c.inventoryRoots()
	if command.Action == "checkout" || command.Action == "pull" {
		status, statusErr := gitrepository.GetStatus(ctx, command.Path, roots)
		if statusErr != nil {
			return protocol.GitWorkspaceWriteResult{}, statusErr
		}
		if status.Dirty {
			return protocol.GitWorkspaceWriteResult{}, errors.New("workspace has uncommitted changes")
		}
	}
	var err error
	switch command.Action {
	case "branch.create":
		err = gitrepository.CreateBranch(ctx, command.Path, command.Branch, command.StartPoint, roots)
	case "branch.rename":
		err = gitrepository.RenameBranch(ctx, command.Path, command.Branch, command.NewBranch, roots)
	case "branch.delete":
		err = gitrepository.DeleteBranch(ctx, command.Path, command.Branch, command.Force, roots)
	case "checkout":
		err = gitrepository.Checkout(ctx, command.Path, command.Ref, command.Detach, roots)
	case "remote.add":
		err = gitrepository.AddRemote(ctx, command.Path, command.Remote, command.URL, roots)
	case "remote.set-url":
		err = gitrepository.SetRemoteURL(ctx, command.Path, command.Remote, command.URL, roots)
	case "remote.remove":
		err = gitrepository.RemoveRemote(ctx, command.Path, command.Remote, roots)
	case "fetch":
		err = gitrepository.Fetch(ctx, command.Path, command.Remote, roots)
	case "pull":
		err = gitrepository.Pull(ctx, command.Path, command.Remote, command.Branch, roots)
	case "push":
		err = gitrepository.Push(ctx, command.Path, command.Remote, command.Ref, command.SetUpstream, roots)
	default:
		return protocol.GitWorkspaceWriteResult{}, fmt.Errorf("unsupported workspace Git action %q", command.Action)
	}
	if err != nil {
		return protocol.GitWorkspaceWriteResult{}, err
	}
	snapshot, err := c.inspectWorkspaceGit(ctx, protocol.GitWorkspaceInspectCommand{WorkspaceID: command.WorkspaceID, Path: command.Path, CommitLimit: 50})
	if err != nil {
		return protocol.GitWorkspaceWriteResult{}, err
	}
	return protocol.GitWorkspaceWriteResult{WorkspaceID: command.WorkspaceID, Action: command.Action, Snapshot: snapshot}, nil
}

func (c *Client) createProject(ctx context.Context, command protocol.GitProjectCreateCommand) (protocol.GitProjectCreateResult, error) {
	destination, err := projectCreateDestination(c.config.CloneRoot, command.Name, command.Destination)
	if err != nil {
		return protocol.GitProjectCreateResult{}, err
	}
	result, err := gitrepository.Create(ctx, gitrepository.CreateOptions{
		ProjectID:          command.ProjectID,
		Path:               destination,
		InitialBranch:      command.InitialBranch,
		RemoteURL:          command.RemoteURL,
		RequireEmptyRemote: command.RequireEmptyRemote,
		InitializeREADME:   command.InitializeREADME,
	}, []string{filepath.Clean(c.config.CloneRoot)})
	if err != nil {
		return protocol.GitProjectCreateResult{}, err
	}
	return protocol.GitProjectCreateResult{
		Path:      result.Path,
		Branch:    result.Branch,
		CommitSHA: result.Head,
		Unborn:    result.Unborn,
		RemoteURL: result.RemoteURL,
	}, nil
}

func (c *Client) deleteProject(ctx context.Context, command protocol.GitProjectDeleteCommand) (protocol.GitProjectDeleteResult, error) {
	projectID := strings.TrimSpace(command.ProjectID)
	if projectID == "" {
		return protocol.GitProjectDeleteResult{}, errors.New("project ID is required")
	}
	if strings.TrimSpace(command.Path) == "" {
		return protocol.GitProjectDeleteResult{}, errors.New("project path is required")
	}
	result, err := gitrepository.DeleteManagedProject(ctx, projectID, command.Path, c.config.CloneRoot)
	if err != nil {
		return protocol.GitProjectDeleteResult{}, err
	}
	return protocol.GitProjectDeleteResult{ProjectID: projectID, WorkspaceID: strings.TrimSpace(command.WorkspaceID), Path: filepath.Clean(command.Path), Removed: result.Deleted}, nil
}

func projectCreateDestination(cloneRoot, projectName, destination string) (string, error) {
	cloneRoot = strings.TrimSpace(cloneRoot)
	if cloneRoot == "" || !filepath.IsAbs(cloneRoot) {
		return "", errors.New("clone root must be absolute")
	}
	cloneRoot, err := filepath.Abs(filepath.Clean(cloneRoot))
	if err != nil {
		return "", fmt.Errorf("resolve clone root: %w", err)
	}
	destination = strings.TrimSpace(destination)
	if destination == "" {
		destination = safeProjectName(projectName)
	}
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(cloneRoot, destination)
	}
	destination, err = filepath.Abs(filepath.Clean(destination))
	if err != nil {
		return "", fmt.Errorf("resolve project destination: %w", err)
	}
	relative, err := filepath.Rel(cloneRoot, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("destination must stay below the configured clone root")
	}
	return destination, nil
}

func safeProjectName(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	name := strings.Trim(builder.String(), "-")
	if name == "" {
		return "repository"
	}
	return name
}

func (c *Client) refreshInventory() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.enqueueInventory(ctx); err != nil {
		c.log.Warn("could not refresh inventory after project creation", "error", err)
	}
}

func (c *Client) execute(ctx context.Context, envelope *protocol.ControlEnvelope) error {
	switch envelope.Kind {
	case "inventory.scan":
		return c.enqueueInventory(ctx)
	case "git.import":
		var command protocol.GitImportCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		if _, err := scanner.Import(ctx, c.config.CloneRoot, command); err != nil {
			return err
		}
		return c.enqueueInventory(ctx)
	case "codex.turn.start":
		var command protocol.StartTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.StartTurn(ctx, command)
	case "codex.turn.rewrite":
		var command protocol.RewriteTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.RewriteTurn(ctx, command)
	case "codex.turn.interrupt":
		var command protocol.InterruptTurnCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.Interrupt(ctx, command)
	case "codex.approval":
		var command protocol.ApprovalDecisionCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.codex.Decide(command)
	case "credentials.configure":
		var command protocol.ConfigureCredentialsCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.configureCredentials(command)
	case "codex.update":
		var command protocol.CodexUpdateCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		if err := c.updateCodexCLI(ctx, command); err != nil {
			return err
		}
		return c.enqueueHeartbeat(ctx)
	case "deploy.start":
		var command protocol.DeployCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.runDeployment(ctx, command)
	case "deploy.rollback":
		var command protocol.RollbackCommand
		if err := json.Unmarshal(envelope.PayloadJSON, &command); err != nil {
			return err
		}
		return c.runRollback(ctx, command)
	default:
		return fmt.Errorf("unsupported operation kind %q", envelope.Kind)
	}
}

func (c *Client) runDeployment(ctx context.Context, command protocol.DeployCommand) error {
	status := func(state, message, resolved string) {
		_ = c.queue("deployment_status", map[string]string{"DeploymentID": command.DeploymentID, "Status": state, "Message": message, "ResolvedCommit": resolved}, true)
	}
	err := c.deployer.Deploy(ctx, command, status)
	if err != nil {
		status("failed", truncate(err.Error(), 8192), "")
	}
	return err
}

func (c *Client) runRollback(ctx context.Context, command protocol.RollbackCommand) error {
	status := func(state, message, resolved string) {
		_ = c.queue("deployment_status", map[string]string{"DeploymentID": command.DeploymentID, "Status": state, "Message": message, "ResolvedCommit": resolved}, true)
	}
	err := c.deployer.Rollback(ctx, command, status)
	if err != nil {
		status("failed", truncate(err.Error(), 8192), "")
	}
	return err
}

func (c *Client) enqueueHeartbeat(ctx context.Context) error {
	hostname, _ := os.Hostname()
	codexPath := c.currentCodexPath()
	heartbeat := protocol.Heartbeat{Hostname: hostname, AgentVersion: buildinfo.Version, CodexVersion: commandVersion(ctx, codexPath), CodexReady: commandAvailable(codexPath), ScanRoots: c.inventoryRoots(), ManagedRoots: c.managedRoots()}
	return c.queue("heartbeat", heartbeat, false)
}

func (c *Client) currentCodexPath() string {
	c.codexPathMu.RLock()
	defer c.codexPathMu.RUnlock()
	return c.codexPath
}

func (c *Client) setCodexPath(path string) {
	c.codexPathMu.Lock()
	c.codexPath = path
	c.codexPathMu.Unlock()
}

func (c *Client) enqueueMetrics(ctx context.Context) error {
	percentages, _ := cpu.PercentWithContext(ctx, 250*time.Millisecond, false)
	memory, _ := mem.VirtualMemoryWithContext(ctx)
	usage, _ := disk.UsageWithContext(ctx, "/")
	average, _ := load.AvgWithContext(ctx)
	interfaces, _ := gnet.IOCountersWithContext(ctx, false)
	metric := protocol.Metrics{}
	if len(percentages) > 0 {
		metric.CPUPercent = percentages[0]
	}
	if memory != nil {
		metric.MemoryPercent = memory.UsedPercent
	}
	if usage != nil {
		metric.DiskPercent = usage.UsedPercent
	}
	if average != nil {
		metric.Load1 = average.Load1
	}
	if len(interfaces) > 0 {
		metric.NetRxBytes = interfaces[0].BytesRecv
		metric.NetTxBytes = interfaces[0].BytesSent
	}
	return c.queue("metrics", metric, false)
}

func (c *Client) enqueueInventory(ctx context.Context) error {
	inventory, err := scanner.Discover(ctx, c.inventoryRoots(), 200)
	if err != nil {
		return err
	}
	return c.queue("inventory", inventory, false)
}

func (c *Client) inventoryRoots() []string {
	roots := make([]string, 0, len(c.config.ScanRoots)+1)
	seen := make(map[string]bool, len(c.config.ScanRoots)+1)
	for _, root := range c.config.ScanRoots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	cloneRoot := filepath.Clean(strings.TrimSpace(c.config.CloneRoot))
	if cloneRoot == "." {
		return roots
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, cloneRoot)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return roots
		}
	}
	return append(roots, cloneRoot)
}

func (c *Client) managedRoots() []string {
	root := filepath.Clean(strings.TrimSpace(c.config.CloneRoot))
	if root == "." || root == "" {
		return nil
	}
	return []string{root}
}

func (c *Client) queue(kind string, payload any, important bool) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	envelope := &protocol.AgentEnvelope{MessageID: uuid.NewString(), ServerID: c.config.ServerID, Kind: kind, OccurredAtUnixMS: time.Now().UnixMilli(), PayloadJSON: raw}
	if important {
		select {
		case c.outbound <- envelope:
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("agent outbound queue is full")
		}
	}
	select {
	case c.outbound <- envelope:
	default:
	}
	return nil
}

func commandVersion(ctx context.Context, command string) string {
	versionContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionContext, command, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func commandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func truncate(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[:size] + "..."
}

var _ = runtime.GOOS
