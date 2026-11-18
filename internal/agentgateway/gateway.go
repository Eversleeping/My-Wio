package agentgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wio-platform/wio/internal/buildinfo"
	"github.com/wio-platform/wio/internal/capabilities"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/sessionservice"
	"github.com/wio-platform/wio/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Gateway struct {
	sessions       *sessionservice.Service
	store          *store.Store
	hub            *realtime.Hub
	vault          *security.Vault
	log            *slog.Logger
	keepaliveEvery time.Duration
	pollEvery      time.Duration
	reconcileAfter time.Duration
	mu             sync.RWMutex
	wakes          map[string]chan struct{}
	connections    map[string]*serverConnections
	metricMu       sync.Mutex
	metricBreaches map[string]map[string]int
	handlers       sync.WaitGroup
}

func New(s *store.Store, hub *realtime.Hub, vault *security.Vault, log *slog.Logger) *Gateway {
	return &Gateway{sessions: &sessionservice.Service{Store: s}, store: s, hub: hub, vault: vault, log: log, keepaliveEvery: 20 * time.Second, pollEvery: 2 * time.Second, reconcileAfter: time.Minute, wakes: make(map[string]chan struct{}), metricBreaches: make(map[string]map[string]int)}
}

func (g *Gateway) Wake(serverID string) {
	g.mu.RLock()
	ch := g.wakes[serverID]
	g.mu.RUnlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (g *Gateway) Connect(stream protocol.AgentServiceConnectServer) error {
	g.handlers.Add(1)
	defer g.handlers.Done()
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	auth := first(md.Get("authorization"))
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return status.Error(codes.Unauthenticated, "missing agent token")
	}
	serverID, err := g.store.AuthenticateAgent(stream.Context(), strings.TrimSpace(auth[7:]))
	if err != nil {
		return status.Error(codes.Unauthenticated, "invalid agent token")
	}
	wake := make(chan struct{}, 1)
	connection := g.registerConnection(stream, serverID, strings.TrimSpace(auth[7:]), wake)
	stream = connection
	defer g.removeConnection(serverID, connection)

	errCh := make(chan error, 1)
	replies := make(chan *protocol.ControlEnvelope, 32)
	g.runHandler(func() { errCh <- g.receiveWithTools(stream, serverID, replies) })
	if err := connection.run(func() error { return g.sendKeepalive(stream, serverID, time.Now()) }); err != nil {
		return err
	}
	// Pending operations are also flushed when Wake is signalled. Keep a short
	// fallback interval for callers that cannot signal the gateway directly.
	ticker := time.NewTicker(g.pollEvery)
	keepalive := time.NewTicker(g.keepaliveEvery)
	defer ticker.Stop()
	defer keepalive.Stop()
	for {
		if err := connection.run(func() error { return g.flush(stream, serverID) }); err != nil {
			return err
		}
		select {
		case err := <-errCh:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-ticker.C:
		case now := <-keepalive.C:
			if err := connection.run(func() error { return g.sendKeepalive(stream, serverID, now) }); err != nil {
				return err
			}
		case reply := <-replies:
			if err := connection.run(func() error { return stream.Send(reply) }); err != nil {
				return err
			}
		case <-wake:
		}
	}
}

func (g *Gateway) sendKeepalive(stream protocol.AgentServiceConnectServer, serverID string, now time.Time) error {
	ids, err := g.store.UnfinishedGeminiOperations(stream.Context(), serverID)
	if err != nil {
		return err
	}
	claudeIDs, err := g.store.UnfinishedClaudeOperations(stream.Context(), serverID)
	if err != nil {
		return err
	}
	// Results are fire-and-forget on the Agent; ask about running operations
	// whose result is overdue so a lost one is replayed or explicitly failed.
	overdue, err := g.store.OverdueRunningOperations(stream.Context(), serverID, now.Add(-g.reconcileAfter))
	if err != nil {
		return err
	}
	payload, err := json.Marshal(protocol.GeminiKeepalive{Capabilities: []string{protocol.ControlCapabilitySessionEnvelopes}, OperationIDs: ids, ClaudeOperationIDs: claudeIDs, ReconcileOperationIDs: overdue})
	if err != nil {
		return err
	}
	return stream.Send(&protocol.ControlEnvelope{Kind: protocol.ControlKindKeepalive, CreatedAtUnixMS: now.UnixMilli(), PayloadJSON: payload})
}

func (g *Gateway) receiveWithTools(stream protocol.AgentServiceConnectServer, serverID string, replies chan<- *protocol.ControlEnvelope) error {
	limit := make(chan struct{}, 8)
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.ServerID != "" && msg.ServerID != serverID {
			return status.Error(codes.PermissionDenied, "server id mismatch")
		}
		handle := func() error {
			if msg.Kind == protocol.AgentKindSessionToolRequest {
				g.receiveSessionTool(stream, serverID, msg, replies, limit)
				return nil
			}
			if err := g.handle(stream.Context(), serverID, msg); err != nil {
				g.log.Warn("agent message rejected", "server_id", serverID, "kind", msg.Kind, "error", err)
			} else if msg.Kind == protocol.AgentKindHeartbeat {
				if connection, ok := stream.(*gatewayConnection); ok {
					connection.heartbeatProcessed = true
				}
				g.Wake(serverID)
			}
			return nil
		}
		if connection, ok := stream.(*gatewayConnection); ok {
			if err := connection.run(handle); err != nil {
				return err
			}
		} else if err := handle(); err != nil {
			return err
		}
	}
}

func (g *Gateway) handle(ctx context.Context, serverID string, msg *protocol.AgentEnvelope) error {
	switch msg.Kind {
	case protocol.AgentKindHeartbeat:
		var h protocol.Heartbeat
		if err := json.Unmarshal(msg.PayloadJSON, &h); err != nil {
			return err
		}
		return g.store.Heartbeat(ctx, serverID, h)
	case protocol.AgentKindMetrics:
		var m protocol.Metrics
		if err := json.Unmarshal(msg.PayloadJSON, &m); err != nil {
			return err
		}
		if err := g.store.SaveMetrics(ctx, serverID, m); err != nil {
			return err
		}
		g.evaluateMetrics(ctx, serverID, m)
		return nil
	case protocol.AgentKindInventory:
		var inv protocol.Inventory
		if err := json.Unmarshal(msg.PayloadJSON, &inv); err != nil {
			return err
		}
		if err := g.store.UpsertInventory(ctx, serverID, inv); err != nil {
			return err
		}
		return g.publish(ctx, protocol.StreamEvent{StreamID: "inventory", Kind: protocol.EventInventoryUpdated, Payload: msg.PayloadJSON})
	case protocol.AgentKindEvent, protocol.AgentKindGeminiEvent, protocol.AgentKindClaudeEvent:
		return g.handleLegacySessionEvent(ctx, serverID, legacyEventEngines[msg.Kind], msg.PayloadJSON)
	case protocol.AgentKindSessionEvent:
		var event protocol.SessionEvent
		if err := json.Unmarshal(msg.PayloadJSON, &event); err != nil {
			return err
		}
		return g.handleSessionEvent(ctx, serverID, event.Engine, event.StreamEvent)
	case protocol.AgentKindClaudeOwnershipLost, protocol.AgentKindClaudeOwnershipActive, protocol.AgentKindGeminiOwnershipLost, protocol.AgentKindGeminiOwnershipActive:
		var ownership protocol.GeminiOwnership
		if err := json.Unmarshal(msg.PayloadJSON, &ownership); err != nil {
			return err
		}
		legacy := legacyOwnershipKinds[msg.Kind]
		return g.handleSessionOwnership(ctx, serverID, legacy.engine, ownership.OperationID, legacy.active)
	case protocol.AgentKindSessionOwnership:
		var ownership protocol.SessionOwnership
		if err := json.Unmarshal(msg.PayloadJSON, &ownership); err != nil {
			return err
		}
		return g.handleSessionOwnership(ctx, serverID, ownership.Engine, ownership.OperationID, ownership.Active)
	case protocol.AgentKindOperationStarted:
		var started protocol.OperationStarted
		if err := json.Unmarshal(msg.PayloadJSON, &started); err != nil {
			return err
		}
		operation, err := g.store.Operation(ctx, started.OperationID)
		if err != nil {
			return err
		}
		if operation.ServerID != serverID {
			return errors.New("operation start server mismatch")
		}
		if operation.Status != "queued" && operation.Status != "delivered" && operation.Status != "running" {
			return nil
		}
		return g.store.MarkRunning(ctx, operation.ID)
	case protocol.AgentKindOperationResult:
		var result protocol.OperationResult
		if err := json.Unmarshal(msg.PayloadJSON, &result); err != nil {
			return err
		}
		return g.handleOperationResult(ctx, serverID, result, false)
	case protocol.AgentKindOperationReconcile:
		var reconcile protocol.OperationReconcile
		if err := json.Unmarshal(msg.PayloadJSON, &reconcile); err != nil {
			return err
		}
		return g.reconcileOperation(ctx, serverID, reconcile)
	case protocol.AgentKindAgentUpdateStatus:
		return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: protocol.EventAgentUpdated, Payload: security.RedactJSON(msg.PayloadJSON)})
	case protocol.AgentKindDeploymentStatus:
		var p protocol.DeploymentStatus
		if err := json.Unmarshal(msg.PayloadJSON, &p); err != nil {
			return err
		}
		deployment, err := g.store.Deployment(ctx, p.DeploymentID)
		if err != nil {
			return fmt.Errorf("resolve Agent deployment: %w", err)
		}
		target, err := g.store.DeploymentTarget(ctx, deployment.TargetID)
		if err != nil {
			return err
		}
		owner := target.ServerID
		if deployment.SnapshotAvailable {
			snapshot, err := g.store.DeploymentSnapshot(ctx, deployment.ID)
			if err != nil {
				return err
			}
			owner = snapshot.ServerID
		}
		if owner != serverID {
			return errors.New("Agent deployment server mismatch")
		}
		if err := g.store.SaveDeploymentStatus(ctx, p); err != nil {
			return err
		}
		payload := security.RedactJSON(msg.PayloadJSON)
		return g.publish(ctx, protocol.StreamEvent{StreamID: p.DeploymentID, Kind: protocol.EventDeploymentPrefix + p.Status, Payload: payload})
	default:
		return errors.New("unsupported agent message kind")
	}
}

func (g *Gateway) flush(stream protocol.AgentServiceConnectServer, serverID string) error {
	// Connect calls flush under the connection slot lock, as does receive.
	if connection, ok := stream.(*gatewayConnection); ok && !connection.heartbeatProcessed {
		return nil
	}
	ops, err := g.store.PendingOperations(stream.Context(), serverID)
	if err != nil || len(ops) == 0 {
		return err
	}
	// Avoid the full server/CLI/profile projection, and skip the lookup when
	// idle. Incoming heartbeats and dispatch share the connection slot lock.
	var agentVersion string
	if err := g.store.DB.GetContext(stream.Context(), &agentVersion, g.store.Q("SELECT agent_version FROM servers WHERE id=?"), serverID); err != nil {
		return err
	}
	for _, op := range ops {
		if op.Kind == protocol.OpAgentIsolationPlan || op.Kind == protocol.OpAgentIsolationInstall {
			if err := g.handleOperationResult(stream.Context(), serverID, protocol.OperationResult{OperationID: op.ID, Status: "failed", Message: "Session isolation has been removed; this legacy operation will not be sent to the Agent"}, op.Status != "queued" || op.DeliveredAt != nil); err != nil {
				return err
			}
			continue
		}
		if op.Kind != protocol.OpAgentUpdate && !buildinfo.MeetsAgentMinimum(agentVersion) {
			message := fmt.Sprintf("Agent %q requires upgrade to %s or newer before this operation can run", agentVersion, buildinfo.MinimumAgentVersion)
			if err := g.failDeploymentOperation(stream.Context(), op, message); err != nil {
				return err
			}
			if err := g.handleOperationResult(stream.Context(), serverID, protocol.OperationResult{OperationID: op.ID, Status: "failed", Message: message}, op.Status != "queued" || op.DeliveredAt != nil); err != nil {
				return err
			}
			continue
		}
		nonReplayableTurn := op.Kind == protocol.OpGeminiTurnStart || op.Kind == protocol.OpGeminiTurnRewrite || op.Kind == protocol.OpClaudeTurnStart
		payload := json.RawMessage(op.Payload)
		if strings.HasPrefix(op.Payload, "v1:") {
			if err := g.vault.Decrypt(op.Payload, &payload); err != nil {
				g.log.Error("could not decrypt operation payload", "operation_id", op.ID, "error", err)
				result := protocol.OperationResult{OperationID: op.ID, Status: "failed", Message: "Control plane could not decrypt the operation payload; reconfigure credentials or retry the operation"}
				if err := g.failDeploymentOperation(stream.Context(), op, result.Message); err != nil {
					return err
				}
				// Use normal result handling for container and credential state,
				// rather than only terminating the transport operation.
				if err := g.handleOperationResult(stream.Context(), serverID, result, false); err != nil {
					return err
				}
				continue
			}
		}
		if requiresTaskArtifacts(op.Kind, payload) {
			var declared capabilities.Declaration
			if err := g.store.DB.GetContext(stream.Context(), &declared, g.store.Q("SELECT (SELECT capabilities FROM server_capabilities WHERE server_id=?)"), serverID); err != nil {
				return err
			}
			if !capabilities.Supports(protocol.CapabilityTaskArtifacts, agentVersion, declared) {
				rejected := protocol.OperationResult{OperationID: op.ID, Status: "failed", Message: "Agent does not declare task artifact support"}
				if err := g.handleOperationResult(stream.Context(), serverID, rejected, op.Status != "queued" || op.DeliveredAt != nil); err != nil {
					return err
				}
				continue
			}
		}
		if op.Kind == protocol.OpAgentAccessUpdate {
			var command protocol.AgentAccessCommand
			if json.Unmarshal(payload, &command) == nil && command.Settings != nil && command.Settings.SessionIsolation {
				if err := g.handleOperationResult(stream.Context(), serverID, protocol.OperationResult{OperationID: op.ID, Status: "failed", Message: "Session isolation has been removed; this legacy setting will not be applied"}, op.Status != "queued" || op.DeliveredAt != nil); err != nil {
					return err
				}
				continue
			}
		}
		codexClaim := isCodexTurnOperation(op.Kind) && op.Status == "queued"
		if nonReplayableTurn {
			// Claim before sending so cancellation or a lost acknowledgement cannot
			// lead to a duplicate CLI execution on reconnect.
			claimed, err := g.store.ClaimGeminiDelivery(stream.Context(), op.ID)
			if err != nil {
				return err
			}
			if !claimed {
				continue
			}
		}
		if codexClaim {
			claimed, err := g.store.ClaimCodexTurnDelivery(stream.Context(), op.ID)
			if err != nil {
				return err
			}
			if !claimed {
				continue
			}
		}
		if err := stream.Send(&protocol.ControlEnvelope{OperationID: op.ID, Kind: op.Kind, CreatedAtUnixMS: op.CreatedAt.UnixMilli(), PayloadJSON: payload}); err != nil {
			return err
		}
		if !nonReplayableTurn && !codexClaim {
			if err := g.store.MarkDelivered(stream.Context(), op.ID); err != nil {
				g.log.Warn("could not mark Agent operation delivered", "operation_id", op.ID, "error", err)
				continue
			}
		}
		g.log.Info(
			"Agent operation delivered",
			"server_id", serverID,
			"operation_id", op.ID,
			"kind", op.Kind,
			"queue_wait_ms", time.Since(op.CreatedAt).Milliseconds(),
		)
		// A cancellation can win after the operation is claimed for sending.
		if isCodexTurnOperation(op.Kind) || nonReplayableTurn {
			updated, err := g.store.Operation(stream.Context(), op.ID)
			if err != nil || updated.Status != "cancelled" {
				continue
			}
			if isCodexTurnOperation(op.Kind) {
				if err := g.queueBestEffortInterrupt(stream.Context(), serverID, op); err != nil {
					g.log.Warn("could not queue best-effort Codex interrupt", "operation_id", op.ID, "error", err)
				}
				continue
			}
			var command struct {
				ThreadID string `json:"thread_id"`
			}
			if err := json.Unmarshal(payload, &command); err != nil {
				return err
			}
			kind, key := protocol.OpGeminiTurnInterrupt, "gemini-cancel:"+op.ID
			var interrupt any = protocol.GeminiInterruptCommand{ThreadID: command.ThreadID, OperationID: op.ID}
			if op.Kind == protocol.OpClaudeTurnStart {
				kind, key = protocol.OpClaudeTurnInterrupt, "claude-cancel:"+op.ID
				interrupt = protocol.ClaudeInterruptCommand{ThreadID: command.ThreadID, OperationID: op.ID}
			}
			if _, err := g.store.QueueResourceOperation(stream.Context(), serverID, kind, interrupt, key, store.OperationResource{ProjectID: op.ProjectID, WorkspaceID: op.WorkspaceID}, false); err != nil {
				return err
			}
			g.Wake(serverID)
		}
	}
	return nil
}

func (g *Gateway) publish(ctx context.Context, event protocol.StreamEvent) error {
	saved, err := g.store.AddEvent(ctx, event)
	if err == nil {
		g.hub.Publish(saved)
	}
	return err
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func requiresTaskArtifacts(kind string, payload json.RawMessage) bool {
	switch kind {
	case protocol.OpTaskArtifactCapture, protocol.OpTaskArtifactHandoff:
		return true
	case protocol.OpCodexTurnStart, protocol.OpClaudeTurnStart, protocol.OpGeminiTurnStart:
		if !bytes.Contains(payload, []byte(`"task_review_snapshot"`)) {
			return false
		}
		var fields struct {
			Review json.RawMessage `json:"task_review_snapshot"`
		}
		return json.Unmarshal(payload, &fields) == nil && len(fields.Review) > 0 && string(fields.Review) != "null"
	}
	return false
}
