package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/claudeadapter"
	"github.com/wio-platform/wio/internal/codexadapter"
	"github.com/wio-platform/wio/internal/deployer"
	"github.com/wio-platform/wio/internal/geminiadapter"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/runtimeaccess"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/sessiontools"
)

const initialReconnectBackoff = time.Second

type Client struct {
	instanceOnce       sync.Once
	instanceID         string
	runtimeAccessCall  func(context.Context, runtimeaccess.Request) (protocol.AgentAccessSettings, error)
	runtimeHelperMu    sync.Mutex
	runtimeHelperTag   string
	toolMu             sync.Mutex
	toolReplies        map[string]chan sessiontools.Reply
	config             Config
	log                *slog.Logger
	outbound           chan *protocol.AgentEnvelope
	codex              *codexadapter.Sessions
	gemini             *geminiadapter.Adapter
	deployer           *deployer.Deployer
	seenMu             sync.Mutex
	seen               map[string]*operationExecution
	updatePending      bool
	codexPathMu        sync.RWMutex
	codexPath          string
	cliInstallMu       sync.Mutex
	geminiVersionMu    sync.Mutex
	geminiVersionPath  string
	geminiVersion      string
	geminiVersionUntil time.Time
	claudeTurns        claudeadapter.Sessions
	claudeMu           sync.RWMutex
	claudeVersionMu    sync.Mutex
	claudeVersionPath  string
	claudeVersion      string
	claudeVersionUntil time.Time
	receiptCleanupMu   sync.Mutex
	lastReceiptCleanup time.Time
	// sentMu guards the one-slot retry buffer and the waiters for tracked
	// messages; both are touched only by queueTracked and sendLoop.
	sentMu      sync.Mutex
	retry       *protocol.AgentEnvelope
	sentWaiters map[string]chan struct{}
	workers     sync.WaitGroup
}

func (c *Client) runtimeInstanceID() string {
	c.instanceOnce.Do(func() { c.instanceID = uuid.NewString() })
	return c.instanceID
}

func NewClient(config Config, log *slog.Logger) *Client {
	codexPath := effectiveCodexPath(config)
	client := &Client{config: config, log: log, outbound: make(chan *protocol.AgentEnvelope, 4096), deployer: deployer.New(config.DockerPath, config.PrerequisiteSocket), seen: make(map[string]*operationExecution), codexPath: codexPath}
	client.codex = codexadapter.NewSessions(codexPath, codexEnvironment(config, log), log, func(event protocol.StreamEvent) error {
		return client.emitSessionEvent(session.Codex, event)
	})
	client.gemini = geminiadapter.New(effectiveGeminiPath(config.StateDir), func(event protocol.StreamEvent) error {
		return client.emitSessionEvent(session.Gemini, event)
	})
	if err := client.loadGeminiCredentials(); err != nil {
		log.Warn("Gemini API configuration could not be loaded")
	}
	client.codex.SetCollaborationTransport(client.callSessionTool)
	return client
}

func (c *Client) Run(ctx context.Context) error {
	ctx = c.processContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		_ = c.codex.Close()
		// Operations persist their final receipts after cancellation. Do not
		// return while they or a reconnect's maintenance tasks still use state.
		c.workers.Wait()
	}()
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

func (c *Client) processContext(ctx context.Context) context.Context {
	return childprocess.WithExtraEnvironment(ctx, c.config.ExtraEnvironment)
}

func (c *Client) startWorker(work func()) {
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		work()
	}()
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
	connection, err := grpc.NewClient(parsed.Host, grpc.WithTransportCredentials(transport), grpc.WithDefaultCallOptions(grpc.ForceCodec(protocol.Codec()), grpc.MaxCallRecvMsgSize(maxAgentMessageBytes), grpc.MaxCallSendMsgSize(maxAgentMessageBytes)))
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
	traffic := make(chan struct{}, 1)
	c.startWorker(func() { watchControlTraffic(streamContext, cancel, traffic, time.Minute) })
	c.pruneOperationReceipts(time.Now())
	// Start the app-server while the Agent connection is becoming ready. The
	// first user turn can then reuse the initialized process instead of paying
	// the process startup and handshake cost on its critical path.
	warmContext, warmCancel := context.WithTimeout(streamContext, 20*time.Second)
	c.startWorker(func() {
		defer warmCancel()
		if err := c.codex.Warm(warmContext); err != nil && warmContext.Err() == nil {
			c.log.Warn("could not warm Codex app-server", "error", err)
		}
	})
	_ = c.enqueueHeartbeat(ctx)
	_ = c.enqueueMetrics(ctx)
	_ = c.enqueueInventory(ctx)
	capabilities := &controlPlaneCapabilities{}
	sendErrors := make(chan error, 1)
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		c.sendLoop(streamContext, stream, sendErrors, capabilities)
		cancel()
	}()
	// The next connection's sendLoop must not start before this one has parked
	// its failed message in the retry slot, or that message would be reordered.
	defer func() {
		cancel()
		<-sendDone
	}()
	c.startWorker(func() { c.periodic(streamContext) })
	connected := false
	for {
		command, err := stream.Recv()
		if err != nil {
			cancel()
			return connected, err
		}
		select {
		case traffic <- struct{}{}:
		default:
		}
		if !connected {
			connected = true
			confirmed, confirmErr := confirmCurrentUpdate(c.config.StateDir)
			if confirmErr != nil {
				c.log.Warn("could not confirm Agent update", "error", confirmErr)
			} else if confirmed {
				_ = c.queue(protocol.AgentKindAgentUpdateStatus, protocol.AgentUpdateStatus{Version: buildinfo.Version, Status: "healthy"}, true)
			}
			c.log.Info("connected to Wio control plane", "server_id", c.config.ServerID)
		}
		select {
		case err := <-sendErrors:
			cancel()
			return connected, err
		default:
		}
		if command.Kind == protocol.ControlKindSessionToolResponse {
			c.receiveSessionToolReply(command.PayloadJSON)
			continue
		}
		if command.Kind == protocol.ControlKindKeepalive && command.OperationID == "" {
			capabilities.observeKeepalive(command.PayloadJSON)
			c.startWorker(func() { c.reconcileGeminiOperations(command.PayloadJSON) })
			c.startWorker(func() { c.reconcileClaudeOperations(command.PayloadJSON) })
			c.startWorker(func() { c.reconcileOperations(command.PayloadJSON) })
			continue
		}
		c.startWorker(func() { c.handleOperation(ctx, command) })
	}
}

func (c *Client) sendLoop(ctx context.Context, stream protocol.AgentServiceConnectClient, errors chan<- error, capabilities *controlPlaneCapabilities) {
	for {
		envelope := c.takeRetry()
		if envelope == nil {
			select {
			case <-ctx.Done():
				return
			case envelope = <-c.outbound:
			}
		}
		wire := envelope
		if capabilities != nil && capabilities.sessionEnvelopes.Load() {
			wire = negotiatedSessionEnvelope(envelope)
		}
		if err := stream.Send(wire); err != nil {
			if status.Code(err) == codes.ResourceExhausted {
				// The message itself is too large for the stream. Retrying the same
				// bytes would kill every future connection, so shrink or drop it.
				envelope = c.replaceOversized(envelope)
			}
			if envelope != nil {
				c.setRetry(envelope)
			}
			select {
			case errors <- err:
			default:
			}
			return
		}
		c.markSent(envelope)
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
			c.pruneOperationReceipts(time.Now())
			c.seenMu.Lock()
			c.pruneCompletedOperations(time.Now())
			c.seenMu.Unlock()
			_ = c.enqueueHeartbeat(ctx)
		case <-metrics.C:
			_ = c.enqueueMetrics(ctx)
		case <-inventory.C:
			_ = c.enqueueInventory(ctx)
		}
	}
}

func (c *Client) queue(kind string, payload any, important bool) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.enqueue(c.newEnvelope(kind, raw), important)
}

func truncate(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[:size] + "..."
}

var _ = runtime.GOOS
