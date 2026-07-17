package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

type Gateway struct {
	store          *store.Store
	hub            *realtime.Hub
	vault          *security.Vault
	log            *slog.Logger
	keepaliveEvery time.Duration
	mu             sync.RWMutex
	wakes          map[string]chan struct{}
	metricMu       sync.Mutex
	metricBreaches map[string]map[string]int
}

func New(s *store.Store, hub *realtime.Hub, vault *security.Vault, log *slog.Logger) *Gateway {
	return &Gateway{store: s, hub: hub, vault: vault, log: log, keepaliveEvery: 20 * time.Second, wakes: make(map[string]chan struct{}), metricBreaches: make(map[string]map[string]int)}
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
	g.mu.Lock()
	g.wakes[serverID] = wake
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if g.wakes[serverID] == wake {
			delete(g.wakes, serverID)
		}
		g.mu.Unlock()
		_, _ = g.store.DB.ExecContext(context.Background(), g.store.Q("UPDATE servers SET status='offline' WHERE id=?"), serverID)
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- g.receive(stream, serverID) }()
	if err := sendKeepalive(stream, time.Now()); err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Second)
	keepalive := time.NewTicker(g.keepaliveEvery)
	defer ticker.Stop()
	defer keepalive.Stop()
	for {
		if err := g.flush(stream, serverID); err != nil {
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
			if err := sendKeepalive(stream, now); err != nil {
				return err
			}
		case <-wake:
		}
	}
}

func sendKeepalive(stream protocol.AgentServiceConnectServer, now time.Time) error {
	return stream.Send(&protocol.ControlEnvelope{Kind: protocol.ControlKindKeepalive, CreatedAtUnixMS: now.UnixMilli()})
}

func (g *Gateway) receive(stream protocol.AgentServiceConnectServer, serverID string) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.ServerID != "" && msg.ServerID != serverID {
			return status.Error(codes.PermissionDenied, "server id mismatch")
		}
		if err := g.handle(stream.Context(), serverID, msg); err != nil {
			g.log.Warn("agent message rejected", "server_id", serverID, "kind", msg.Kind, "error", err)
		}
	}
}

func (g *Gateway) handle(ctx context.Context, serverID string, msg *protocol.AgentEnvelope) error {
	switch msg.Kind {
	case "heartbeat":
		var h protocol.Heartbeat
		if err := json.Unmarshal(msg.PayloadJSON, &h); err != nil {
			return err
		}
		return g.store.Heartbeat(ctx, serverID, h)
	case "metrics":
		var m protocol.Metrics
		if err := json.Unmarshal(msg.PayloadJSON, &m); err != nil {
			return err
		}
		if err := g.store.SaveMetrics(ctx, serverID, m); err != nil {
			return err
		}
		g.evaluateMetrics(ctx, serverID, m)
		return nil
	case "inventory":
		var inv protocol.Inventory
		if err := json.Unmarshal(msg.PayloadJSON, &inv); err != nil {
			return err
		}
		if err := g.store.UpsertInventory(ctx, serverID, inv); err != nil {
			return err
		}
		return g.publish(ctx, protocol.StreamEvent{StreamID: "inventory", Kind: "inventory.updated", Payload: msg.PayloadJSON})
	case "event":
		var event protocol.StreamEvent
		if err := json.Unmarshal(msg.PayloadJSON, &event); err != nil {
			return err
		}
		event.Payload = security.RedactJSON(event.Payload)
		if strings.Contains(event.Kind, "approval.requested") {
			_ = g.upsertApproval(ctx, event)
		}
		if event.Kind == "thread.bound" {
			_ = g.bindCodexThread(ctx, event)
		}
		if event.Kind == "codex.turn.started" || event.Kind == "turn.accepted" {
			_ = g.store.SetThreadStatus(ctx, event.StreamID, "running")
		}
		if event.Kind == "codex.turn.completed" {
			_ = g.store.SetThreadStatus(ctx, event.StreamID, "idle")
		}
		return g.publish(ctx, event)
	case "operation_result":
		var result protocol.OperationResult
		if err := json.Unmarshal(msg.PayloadJSON, &result); err != nil {
			return err
		}
		operation, err := g.store.Operation(ctx, result.OperationID)
		if err != nil {
			return err
		}
		if operation.ServerID != serverID {
			return errors.New("operation result server mismatch")
		}
		if err := g.store.CompleteOperation(ctx, result); err != nil {
			return err
		}
		if operation.Kind == "codex.turn.start" && result.Status == "failed" {
			if err := g.failCodexTurn(ctx, operation, result); err != nil {
				return err
			}
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: "operation." + result.Status, Payload: security.RedactJSON(payload)})
	case "agent_update_status":
		return g.publish(ctx, protocol.StreamEvent{StreamID: serverID, Kind: "agent.updated", Payload: security.RedactJSON(msg.PayloadJSON)})
	case "deployment_status":
		var p struct{ DeploymentID, Status, Message, ResolvedCommit string }
		if err := json.Unmarshal(msg.PayloadJSON, &p); err != nil {
			return err
		}
		_, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE deployments SET status=?,message=?,resolved_commit=CASE WHEN ?='' THEN resolved_commit ELSE ? END,started_at=CASE WHEN ?='preparing' THEN ? ELSE started_at END,finished_at=CASE WHEN ? IN ('succeeded','failed','rolled_back','canceled') THEN ? ELSE finished_at END WHERE id=?"), p.Status, p.Message, p.ResolvedCommit, p.ResolvedCommit, p.Status, time.Now().UTC(), p.Status, time.Now().UTC(), p.DeploymentID)
		if err != nil {
			return err
		}
		payload := security.RedactJSON(msg.PayloadJSON)
		return g.publish(ctx, protocol.StreamEvent{StreamID: p.DeploymentID, Kind: "deployment." + p.Status, Payload: payload})
	default:
		return errors.New("unsupported agent message kind")
	}
}

func (g *Gateway) failCodexTurn(ctx context.Context, operation store.Operation, result protocol.OperationResult) error {
	var command protocol.StartTurnCommand
	if err := json.Unmarshal([]byte(operation.Payload), &command); err != nil {
		return err
	}
	if command.ThreadID == "" {
		return errors.New("Codex turn operation has no thread id")
	}
	if err := g.store.SetThreadStatus(ctx, command.ThreadID, "failed"); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"operation_id": result.OperationID, "text": result.Message})
	if err != nil {
		return err
	}
	return g.publish(ctx, protocol.StreamEvent{StreamID: command.ThreadID, Kind: "codex.turn.failed", Payload: security.RedactJSON(payload)})
}

func (g *Gateway) flush(stream protocol.AgentServiceConnectServer, serverID string) error {
	ops, err := g.store.PendingOperations(stream.Context(), serverID)
	if err != nil {
		return err
	}
	for _, op := range ops {
		payload := json.RawMessage(op.Payload)
		if strings.HasPrefix(op.Payload, "v1:") {
			if err := g.vault.Decrypt(op.Payload, &payload); err != nil {
				g.log.Error("could not decrypt operation payload", "operation_id", op.ID, "error", err)
				continue
			}
		}
		if err := stream.Send(&protocol.ControlEnvelope{OperationID: op.ID, Kind: op.Kind, CreatedAtUnixMS: op.CreatedAt.UnixMilli(), PayloadJSON: payload}); err != nil {
			return err
		}
		_ = g.store.MarkDelivered(stream.Context(), op.ID)
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

func (g *Gateway) upsertApproval(ctx context.Context, event protocol.StreamEvent) error {
	var p struct {
		RequestID, Kind string
		Detail          json.RawMessage
	}
	if json.Unmarshal(event.Payload, &p) != nil || p.RequestID == "" {
		return nil
	}
	if len(p.Detail) == 0 {
		p.Detail = event.Payload
	}
	_, err := g.store.DB.ExecContext(ctx, g.store.Q(`INSERT INTO approvals(id,thread_id,request_id,kind,detail,status,expires_at) VALUES(?,?,?,?,?,'pending',?) ON CONFLICT(thread_id,request_id) DO NOTHING`), store.NewID(), event.StreamID, p.RequestID, p.Kind, string(p.Detail), time.Now().UTC().Add(15*time.Minute))
	return err
}

func (g *Gateway) bindCodexThread(ctx context.Context, event protocol.StreamEvent) error {
	var p struct {
		CodexThreadID string `json:"codex_thread_id"`
	}
	if json.Unmarshal(event.Payload, &p) != nil || p.CodexThreadID == "" {
		return nil
	}
	_, err := g.store.DB.ExecContext(ctx, g.store.Q("UPDATE codex_threads SET codex_thread_id=?,updated_at=? WHERE id=?"), p.CodexThreadID, time.Now().UTC(), event.StreamID)
	return err
}

func (g *Gateway) evaluateMetrics(ctx context.Context, serverID string, m protocol.Metrics) {
	checks := map[string]struct {
		hit      bool
		title    string
		severity string
	}{"cpu_high": {m.CPUPercent >= 90, "CPU usage above 90%", "warning"}, "memory_high": {m.MemoryPercent >= 90, "Memory usage above 90%", "warning"}, "disk_high": {m.DiskPercent >= 90, "Disk usage above 90%", "critical"}}
	g.metricMu.Lock()
	defer g.metricMu.Unlock()
	if g.metricBreaches[serverID] == nil {
		g.metricBreaches[serverID] = map[string]int{}
	}
	for kind, check := range checks {
		if check.hit {
			g.metricBreaches[serverID][kind]++
		} else {
			g.metricBreaches[serverID][kind] = 0
			_, _ = g.store.DB.ExecContext(ctx, g.store.Q("UPDATE alerts SET status='resolved',resolved_at=? WHERE server_id=? AND kind=? AND status='open'"), time.Now().UTC(), serverID, kind)
		}
		if g.metricBreaches[serverID][kind] == 3 {
			var n int
			_ = g.store.DB.GetContext(ctx, &n, g.store.Q("SELECT COUNT(*) FROM alerts WHERE server_id=? AND kind=? AND status='open'"), serverID, kind)
			if n == 0 {
				_, _ = g.store.DB.ExecContext(ctx, g.store.Q("INSERT INTO alerts(id,server_id,kind,severity,title) VALUES(?,?,?,?,?)"), uuid.NewString(), serverID, kind, check.severity, check.title)
			}
		}
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
