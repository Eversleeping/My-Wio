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

	"github.com/wio-platform/wio/internal/codexadapter"
	"github.com/wio-platform/wio/internal/deployer"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/scanner"
)

const agentVersion = "0.1.0"

type Client struct {
	config   Config
	log      *slog.Logger
	outbound chan *protocol.AgentEnvelope
	codex    *codexadapter.Adapter
	deployer *deployer.Deployer
	seenMu   sync.Mutex
	seen     map[string]bool
}

func NewClient(config Config, log *slog.Logger) *Client {
	client := &Client{config: config, log: log, outbound: make(chan *protocol.AgentEnvelope, 4096), deployer: deployer.New(config.DockerPath), seen: make(map[string]bool)}
	client.codex = codexadapter.New(config.CodexPath, log, func(event protocol.StreamEvent) error {
		return client.queue("event", event, true)
	})
	return client
}

func (c *Client) Run(ctx context.Context) error {
	defer c.codex.Close()
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.connect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
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

func (c *Client) connect(ctx context.Context) error {
	parsed, err := url.Parse(c.config.ControlURL)
	if err != nil {
		return err
	}
	var transport credentials.TransportCredentials
	if parsed.Scheme == "https" {
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname(), InsecureSkipVerify: c.config.InsecureSkipVerify})
	} else {
		transport = insecure.NewCredentials()
	}
	connection, err := grpc.NewClient(parsed.Host, grpc.WithTransportCredentials(transport), grpc.WithDefaultCallOptions(grpc.ForceCodec(protocol.Codec()), grpc.MaxCallRecvMsgSize(8<<20), grpc.MaxCallSendMsgSize(8<<20)))
	if err != nil {
		return err
	}
	defer connection.Close()
	streamContext, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.config.AgentToken))
	defer cancel()
	stream, err := protocol.NewAgentServiceClient(connection).Connect(streamContext)
	if err != nil {
		return err
	}
	c.log.Info("connected to Wio control plane", "server_id", c.config.ServerID)
	_ = c.enqueueHeartbeat(ctx)
	_ = c.enqueueMetrics(ctx)
	_ = c.enqueueInventory(ctx)
	sendErrors := make(chan error, 1)
	go c.sendLoop(streamContext, stream, sendErrors)
	go c.periodic(streamContext)
	for {
		command, err := stream.Recv()
		if err != nil {
			cancel()
			return err
		}
		go c.handleOperation(ctx, command)
		select {
		case err := <-sendErrors:
			cancel()
			return err
		default:
		}
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
	if c.seen[envelope.OperationID] {
		c.seenMu.Unlock()
		return
	}
	c.seen[envelope.OperationID] = true
	c.seenMu.Unlock()
	timeout := 10 * time.Minute
	if strings.HasPrefix(envelope.Kind, "deploy.") {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := c.execute(ctx, envelope)
	status := "succeeded"
	message := ""
	if err != nil {
		status = "failed"
		message = err.Error()
		c.log.Warn("operation failed", "operation_id", envelope.OperationID, "kind", envelope.Kind, "error", err)
	}
	_ = c.queue("operation_result", protocol.OperationResult{OperationID: envelope.OperationID, Status: status, Message: truncate(message, 8192)}, true)
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
	heartbeat := protocol.Heartbeat{Hostname: hostname, AgentVersion: agentVersion, CodexVersion: commandVersion(ctx, c.config.CodexPath), CodexReady: commandAvailable(c.config.CodexPath), ScanRoots: c.config.ScanRoots}
	return c.queue("heartbeat", heartbeat, false)
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
	inventory, err := scanner.Discover(ctx, c.config.ScanRoots, 200)
	if err != nil {
		return err
	}
	return c.queue("inventory", inventory, false)
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
