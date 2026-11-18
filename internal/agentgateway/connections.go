package agentgateway

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wio-platform/wio/internal/protocol"
)

// Wait joins connection cleanup, receivers and collaboration tools after the
// transport has stopped and all Connect handlers have returned. No new Connect
// calls may start while waiting.
func (g *Gateway) Wait() { g.handlers.Wait() }

func (g *Gateway) runHandler(work func()) {
	g.handlers.Add(1)
	go func() {
		defer g.handlers.Done()
		work()
	}()
}

// A slot survives reconnects. Its mutex serializes delivery, incoming writes,
// replacement and offline cleanup for a single server.
type serverConnections struct {
	mu      sync.Mutex
	current atomic.Pointer[gatewayConnection]
}

type gatewayConnection struct {
	protocol.AgentServiceConnectServer
	ctx      context.Context
	cancel   context.CancelFunc
	slot     *serverConnections
	gateway  *Gateway
	serverID string
	token    string
	sends    chan gatewaySend
	// Protected by slot.mu; a reconnect must establish its own heartbeat.
	heartbeatProcessed bool
}

func (c *gatewayConnection) Context() context.Context { return c.ctx }

type gatewaySend struct {
	message *protocol.ControlEnvelope
	result  chan error
}

// A single sender owns the gRPC stream. The Connect handler may return on
// replacement, which cancels the transport and releases a blocked Send.
func (c *gatewayConnection) sendLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case request := <-c.sends:
			if err := c.ctx.Err(); err != nil {
				request.result <- err
				return
			}
			request.result <- c.AgentServiceConnectServer.Send(request.message)
		}
	}
}

func (c *gatewayConnection) Send(message *protocol.ControlEnvelope) error {
	request := gatewaySend{message: message, result: make(chan error, 1)}
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case c.sends <- request:
	}
	select {
	case err := <-request.result:
		return err
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

func (c *gatewayConnection) run(action func() error) error {
	c.slot.mu.Lock()
	defer c.slot.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if c.slot.current.Load() != c {
		return status.Error(codes.Canceled, "Agent stream superseded or closed")
	}
	// Enrollment repair and revocation change credentials without restarting
	// the gateway. Recheck on every poll and incoming message.
	id, err := c.gateway.store.AuthenticateAgent(c.ctx, c.token)
	// Cancellation may race with the query, including a successful result.
	// Preserve the context error instead of reporting a credential failure.
	if ctxErr := c.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	// AuthenticateAgent uses sql.ErrNoRows for invalid or revoked tokens.
	// Other storage failures must remain retryable, without invalidating auth.
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil || id != c.serverID {
		c.cancel()
		return status.Error(codes.Unauthenticated, "Agent token is no longer valid")
	}
	return action()
}

func (g *Gateway) registerConnection(stream protocol.AgentServiceConnectServer, serverID, token string, wake chan struct{}) *gatewayConnection {
	g.mu.Lock()
	if g.connections == nil {
		g.connections = make(map[string]*serverConnections)
	}
	slot := g.connections[serverID]
	if slot == nil {
		slot = &serverConnections{}
		g.connections[serverID] = slot
	}
	g.mu.Unlock()
	ctx, cancel := context.WithCancel(stream.Context())
	c := &gatewayConnection{AgentServiceConnectServer: stream, ctx: ctx, cancel: cancel, slot: slot, gateway: g, serverID: serverID, token: token, sends: make(chan gatewaySend)}
	if old := slot.current.Load(); old != nil {
		old.cancel()
	}
	slot.mu.Lock()
	if old := slot.current.Swap(c); old != nil {
		old.cancel()
	}
	g.mu.Lock()
	g.wakes[serverID] = wake
	g.mu.Unlock()
	slot.mu.Unlock()
	g.runHandler(c.sendLoop)
	return c
}

func (g *Gateway) removeConnection(serverID string, c *gatewayConnection) {
	c.cancel()
	c.slot.mu.Lock()
	defer c.slot.mu.Unlock()
	if c.slot.current.Load() != c {
		return
	}
	c.slot.current.Store(nil)
	g.mu.Lock()
	delete(g.wakes, serverID)
	g.mu.Unlock()
	g.metricMu.Lock()
	delete(g.metricBreaches, serverID)
	g.metricMu.Unlock()
	_, _ = g.store.DB.ExecContext(context.Background(), g.store.Q("UPDATE servers SET status='offline' WHERE id=?"), serverID)
}
