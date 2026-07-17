package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

type keepaliveStream struct {
	ctx  context.Context
	sent chan *protocol.ControlEnvelope
}

func (s *keepaliveStream) Send(message *protocol.ControlEnvelope) error {
	s.sent <- message
	return nil
}

func (s *keepaliveStream) Recv() (*protocol.AgentEnvelope, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *keepaliveStream) SetHeader(metadata.MD) error  { return nil }
func (s *keepaliveStream) SendHeader(metadata.MD) error { return nil }
func (s *keepaliveStream) SetTrailer(metadata.MD)       {}
func (s *keepaliveStream) Context() context.Context     { return s.ctx }
func (s *keepaliveStream) SendMsg(any) error            { return nil }
func (s *keepaliveStream) RecvMsg(any) error            { return nil }

func TestConnectSendsDownlinkKeepalive(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "build-01", []string{"/srv"}, "enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnrollServer(ctx, enrollment, "build-01.local", "agent-token"); err != nil {
		t.Fatal(err)
	}

	streamContext, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer agent-token")))
	stream := &keepaliveStream{ctx: streamContext, sent: make(chan *protocol.ControlEnvelope, 1)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway := New(database, realtime.New(), security.DevVault(), log)
	gateway.keepaliveEvery = 10 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- gateway.Connect(stream) }()

	var first *protocol.ControlEnvelope
	select {
	case message := <-stream.sent:
		first = message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial downlink keepalive")
	}
	if first.Kind != protocol.ControlKindKeepalive || first.OperationID != "" || first.CreatedAtUnixMS == 0 {
		t.Fatalf("unexpected initial keepalive: %#v", first)
	}
	select {
	case message := <-stream.sent:
		if message.Kind != protocol.ControlKindKeepalive || message.CreatedAtUnixMS <= first.CreatedAtUnixMS {
			t.Fatalf("unexpected periodic keepalive: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for periodic downlink keepalive")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not stop after stream cancellation")
	}
}

func TestOperationResultPublishesRealtimeEvent(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "build-01", []string{"/srv"}, "result-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "result-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "build-01.local", "result-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]any{}, "result-operation")
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.New()
	subscriptionID, events := hub.Subscribe()
	t.Cleanup(func() { hub.Unsubscribe(subscriptionID) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway := New(database, hub, security.DevVault(), log)
	result := protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "network timeout"}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Kind != "operation.failed" || event.StreamID != server.ID {
			t.Fatalf("unexpected realtime event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for operation result event")
	}
}
