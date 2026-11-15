package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	payload, err := json.Marshal(protocol.GeminiKeepalive{OperationIDs: ids, ClaudeOperationIDs: claudeIDs, ReconcileOperationIDs: overdue})
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
	case protocol.AgentKindGeminiEvent:
		return g.handleGeminiEvent(ctx, serverID, msg.PayloadJSON)
	case protocol.AgentKindClaudeEvent:
		return g.handleClaudeEvent(ctx, serverID, msg.PayloadJSON)
	case protocol.AgentKindClaudeOwnershipLost, protocol.AgentKindClaudeOwnershipActive:
		var ownership protocol.GeminiOwnership
		if err := json.Unmarshal(msg.PayloadJSON, &ownership); err != nil {
			return err
		}
		return g.updateClaudeOwnership(ctx, serverID, ownership.OperationID, msg.Kind == protocol.AgentKindClaudeOwnershipActive)
	case protocol.AgentKindGeminiOwnershipLost:
		var lost protocol.GeminiOwnership
		if err := json.Unmarshal(msg.PayloadJSON, &lost); err != nil {
			return err
		}
		return g.markGeminiOwnershipLost(ctx, serverID, lost.OperationID)
	case protocol.AgentKindGeminiOwnershipActive:
		var active protocol.GeminiOwnership
		if err := json.Unmarshal(msg.PayloadJSON, &active); err != nil {
			return err
		}
		return g.markGeminiOwnershipActive(ctx, serverID, active.OperationID)
	case protocol.AgentKindEvent:
		var event protocol.StreamEvent
		if err := json.Unmarshal(msg.PayloadJSON, &event); err != nil {
			return err
		}
		thread, err := g.store.Thread(ctx, event.StreamID)
		if err != nil {
			return fmt.Errorf("resolve Agent event thread: %w", err)
		}
		if thread.ServerID != serverID {
			return errors.New("Agent event server mismatch")
		}
		if event.Kind == protocol.EventCodexThreadTokenUsageUpdated {
			event.Payload = codexUsagePayload(event.Payload)
		}
		event.Payload = security.RedactJSON(event.Payload)
		if event.Kind != protocol.EventUserMessage {
			cancelled, err := g.store.HasPendingTurnCancellation(ctx, event.StreamID)
			if err != nil {
				return err
			}
			if cancelled {
				// A queued turn can cross the Agent boundary just as the UI
				// cancels it. Its late notifications must not resurrect the
				// cancelled session or leak stale output into the conversation.
				return nil
			}
		}
		if strings.Contains(event.Kind, protocol.EventApprovalRequested) {
			if err := g.upsertApproval(ctx, event); err != nil {
				g.log.Error("could not persist approval request", "thread_id", event.StreamID, "error", err)
			}
		}
		if event.Kind == protocol.EventThreadBound {
			_ = g.bindCodexThread(ctx, event)
		}
		if event.Kind == protocol.EventCodexServerRequestResolved {
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if err := json.Unmarshal(event.Payload, &params); err != nil {
				return err
			}
			var requestID string
			if json.Unmarshal(params.RequestID, &requestID) != nil {
				requestID = string(params.RequestID)
			}
			if _, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE approvals SET status='resolved',decision='cancelled',resolved_at=? WHERE thread_id=? AND request_id=? AND status='pending'"), time.Now().UTC(), event.StreamID, requestID); err != nil {
				return err
			}
		}
		if event.Kind == protocol.EventCodexThreadNameUpdated {
			_ = g.updateThreadTitle(ctx, event)
		}
		if event.Kind == protocol.EventCodexTurnStarted || event.Kind == protocol.EventTurnAccepted {
			if event.Kind == protocol.EventTurnAccepted {
				if err := g.acceptCodexTurn(ctx, event); err != nil {
					return err
				}
			}
			saved, err := g.store.AddEvent(ctx, event)
			if err != nil {
				return err
			}
			if err := g.store.SetThreadStatus(ctx, event.StreamID, "running"); err != nil {
				return err
			}
			_ = g.store.MarkScheduledTaskByThread(ctx, event.StreamID, "running", "")
			g.hub.Publish(saved)
			return nil
		}
		if event.Kind == protocol.EventCodexTurnCompleted || event.Kind == protocol.EventCodexTurnCancelled {
			_ = g.setThreadTitleFromFirstResponse(ctx, event.StreamID)
			status := completedTurnStatus(event.Payload)
			if event.Kind == protocol.EventCodexTurnCancelled {
				status = "interrupted"
			}
			_ = g.store.SetThreadStatus(ctx, event.StreamID, status)
			_ = g.store.MarkScheduledTaskByThread(ctx, event.StreamID, scheduledTurnStatus(status), scheduledTurnMessage(event.Payload))
			_ = g.store.ResolvePendingApprovals(ctx, event.StreamID, "cancelled")
		}
		if event.Kind == protocol.EventCodexTurnFailed {
			_ = g.store.SetThreadStatus(ctx, event.StreamID, "failed")
			_ = g.store.MarkScheduledTaskByThread(ctx, event.StreamID, "failed", scheduledTurnMessage(event.Payload))
			_ = g.store.ResolvePendingApprovals(ctx, event.StreamID, "cancelled")
		}
		return g.publish(ctx, event)
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
	ops, err := g.store.PendingOperations(stream.Context(), serverID)
	if err != nil {
		return err
	}
	for _, op := range ops {
		geminiTurn := op.Kind == protocol.OpGeminiTurnStart || op.Kind == protocol.OpGeminiTurnRewrite || op.Kind == protocol.OpClaudeTurnStart
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
		if geminiTurn {
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
		if err := stream.Send(&protocol.ControlEnvelope{OperationID: op.ID, Kind: op.Kind, CreatedAtUnixMS: op.CreatedAt.UnixMilli(), PayloadJSON: payload}); err != nil {
			return err
		}
		if !geminiTurn {
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
		// Only Codex turns need a post-delivery state lookup: a cancellation can
		// win the race between Send and MarkDelivered, and then needs a best-effort
		// interrupt. Other operation types do not consume this status, so skipping
		// their lookup removes one database round trip per delivered operation.
		if isCodexTurnOperation(op.Kind) {
			updated, statusErr := g.store.Operation(stream.Context(), op.ID)
			if statusErr == nil && updated.Status == "cancelled" {
				if err := g.queueBestEffortInterrupt(stream.Context(), serverID, op); err != nil {
					g.log.Warn("could not queue best-effort Codex interrupt", "operation_id", op.ID, "error", err)
				}
			}
		}
		if op.Kind == protocol.OpGeminiTurnStart || op.Kind == protocol.OpGeminiTurnRewrite {
			updated, statusErr := g.store.Operation(stream.Context(), op.ID)
			if statusErr == nil && updated.Status == "cancelled" {
				var command protocol.GeminiTurnCommand
				if err := json.Unmarshal([]byte(op.Payload), &command); err != nil {
					return err
				}
				_, err := g.store.QueueResourceOperation(stream.Context(), serverID, protocol.OpGeminiTurnInterrupt, protocol.GeminiInterruptCommand{ThreadID: command.ThreadID, OperationID: op.ID}, "gemini-cancel:"+op.ID, store.OperationResource{ProjectID: op.ProjectID, WorkspaceID: op.WorkspaceID}, false)
				if err != nil {
					return err
				}
				g.Wake(serverID)
			}
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
