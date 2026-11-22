package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"google.golang.org/grpc"
)

type reconnectControl struct {
	connections atomic.Int32
	results     chan protocol.OperationResult
	heartbeats  chan int32
}

func (s *reconnectControl) Connect(stream protocol.AgentServiceConnectServer) error {
	index := s.connections.Add(1)
	if err := stream.Send(&protocol.ControlEnvelope{Kind: protocol.ControlKindKeepalive, PayloadJSON: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	// Unsupported commands produce a receipt without starting any model or tool.
	if err := stream.Send(&protocol.ControlEnvelope{OperationID: fmt.Sprintf("reconnect-probe-%d", index), Kind: "test.unsupported", PayloadJSON: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	for {
		message, err := stream.Recv()
		if err != nil {
			return err
		}
		if message.Kind == protocol.AgentKindOperationResult {
			var result protocol.OperationResult
			if err := json.Unmarshal(message.PayloadJSON, &result); err != nil {
				return err
			}
			select {
			case s.results <- result:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
		if message.Kind == protocol.AgentKindHeartbeat {
			select {
			case s.heartbeats <- index:
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
	}
}

func TestControlReconnectWhileVersionProbeIsBlocked(t *testing.T) {
	control := &reconnectControl{results: make(chan protocol.OperationResult, 16), heartbeats: make(chan int32, 16)}
	start := func(address string) (*grpc.Server, string) {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer(grpc.ForceServerCodec(protocol.Codec()))
		protocol.RegisterAgentServiceServer(server, control)
		go server.Serve(listener)
		return server, listener.Addr().String()
	}
	server, address := start("127.0.0.1:0")
	defer func() { server.Stop() }()
	oldRead := readGeminiUpdateVersion
	entered := make(chan struct{}, 1)
	readGeminiUpdateVersion = func(ctx context.Context, _ string) string {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ""
	}
	defer func() { readGeminiUpdateVersion = oldRead }()
	client := NewClient(Config{ControlURL: "http://" + address, StateDir: t.TempDir(), CloneRoot: t.TempDir(), CodexPath: "wio-nonexistent-test-cli", ServerID: "test-reconnect"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Agent did not stop cleanly: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Agent maintenance did not stop after cancellation")
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("version probe did not start")
	}
	for generation := 1; generation <= 2; generation++ {
		want := fmt.Sprintf("reconnect-probe-%d", generation)
		deadline := time.NewTimer(5 * time.Second)
		found := false
		for !found {
			select {
			case result := <-control.results:
				found = result.OperationID == want && result.Status == "failed"
			case <-deadline.C:
				t.Fatalf("connection %d could not process control traffic while telemetry was blocked", generation)
			}
		}
		deadline.Stop()
		select {
		case connection := <-control.heartbeats:
			if connection != int32(generation) {
				t.Fatalf("heartbeat belongs to stale connection %d", connection)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("connection %d did not send a heartbeat while version probing was blocked", generation)
		}
		if generation == 1 {
			server.Stop()
			server, _ = start(address)
		}
	}
}
