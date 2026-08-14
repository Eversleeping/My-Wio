package agentgateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

type keepaliveStream struct {
	ctx      context.Context
	sent     chan *protocol.ControlEnvelope
	recvDone chan struct{}
	onSend   func(*protocol.ControlEnvelope) error
}

func (s *keepaliveStream) Send(message *protocol.ControlEnvelope) error {
	if s.onSend != nil {
		if err := s.onSend(message); err != nil {
			return err
		}
	}
	s.sent <- message
	return nil
}

func (s *keepaliveStream) Recv() (*protocol.AgentEnvelope, error) {
	<-s.ctx.Done()
	if s.recvDone != nil {
		close(s.recvDone)
	}
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

func TestConnectWakeDeliversQueuedOperationWithoutWaitingForFallback(t *testing.T) {
	database, server := gatewayTestServer(t, "wake-agent-token")
	streamContext, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer wake-agent-token")))
	defer cancel()
	stream := &keepaliveStream{ctx: streamContext, sent: make(chan *protocol.ControlEnvelope, 4)}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	gateway.keepaliveEvery = time.Hour
	gateway.pollEvery = time.Hour
	done := make(chan error, 1)
	go func() { done <- gateway.Connect(stream) }()

	select {
	case <-stream.sent:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial keepalive")
	}
	operationID, err := database.QueueOperation(context.Background(), server.ID, "inventory.scan", map[string]any{}, "wake-operation")
	if err != nil {
		t.Fatal(err)
	}
	gateway.Wake(server.ID)
	select {
	case message := <-stream.sent:
		if message.OperationID != operationID || message.Kind != "inventory.scan" {
			t.Fatalf("unexpected operation: %#v", message)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("wake did not deliver queued operation promptly")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not stop after stream cancellation")
	}
}

func TestConnectFallbackEventuallyDeliversOperationWithoutWake(t *testing.T) {
	database, server := gatewayTestServer(t, "fallback-agent-token")
	streamContext, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer fallback-agent-token")))
	defer cancel()
	stream := &keepaliveStream{ctx: streamContext, sent: make(chan *protocol.ControlEnvelope, 4)}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	gateway.keepaliveEvery = time.Hour
	gateway.pollEvery = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- gateway.Connect(stream) }()

	select {
	case <-stream.sent:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial keepalive")
	}
	operationID, err := database.QueueOperation(context.Background(), server.ID, "inventory.scan", map[string]any{}, "fallback-operation")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-stream.sent:
		if message.OperationID != operationID {
			t.Fatalf("unexpected operation: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback poll did not deliver queued operation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not stop after stream cancellation")
	}
}

func TestConnectRedeliversTimedOutDeliveredOperationAfterReconnect(t *testing.T) {
	database, server := gatewayTestServer(t, "reconnect-agent-token")
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	gateway.keepaliveEvery = time.Hour
	gateway.pollEvery = time.Hour

	firstContext, firstCancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer reconnect-agent-token")))
	defer firstCancel()
	firstRecvDone := make(chan struct{})
	firstStream := &keepaliveStream{ctx: firstContext, sent: make(chan *protocol.ControlEnvelope, 4), recvDone: firstRecvDone}
	firstDone := make(chan error, 1)
	go func() { firstDone <- gateway.Connect(firstStream) }()
	select {
	case <-firstStream.sent:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial keepalive on first connection")
	}

	operationID, err := database.QueueOperation(context.Background(), server.ID, "inventory.scan", map[string]any{}, "reconnect-operation")
	if err != nil {
		t.Fatal(err)
	}
	gateway.Wake(server.ID)
	select {
	case message := <-firstStream.sent:
		if message.OperationID != operationID {
			t.Fatalf("first connection received unexpected operation: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first operation delivery")
	}

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		operation, err := database.Operation(context.Background(), operationID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.Status == "delivered" && operation.DeliveredAt != nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("first connection did not mark the operation delivered")
		case <-ticker.C:
		}
	}
	if _, err := database.DB.ExecContext(context.Background(), database.Q("UPDATE agent_operations SET delivered_at=? WHERE id=?"), time.Now().UTC().Add(-31*time.Second), operationID); err != nil {
		t.Fatal(err)
	}

	firstCancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first connection returned %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first connection did not stop after cancellation")
	}
	select {
	case <-firstRecvDone:
	case <-time.After(time.Second):
		t.Fatal("first connection receive loop did not stop after cancellation")
	}

	secondContext, secondCancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer reconnect-agent-token")))
	defer secondCancel()
	secondRecvDone := make(chan struct{})
	secondStream := &keepaliveStream{ctx: secondContext, sent: make(chan *protocol.ControlEnvelope, 4), recvDone: secondRecvDone}
	secondDone := make(chan error, 1)
	go func() { secondDone <- gateway.Connect(secondStream) }()
	select {
	case <-secondStream.sent:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial keepalive on reconnected stream")
	}
	select {
	case message := <-secondStream.sent:
		if message.OperationID != operationID || message.Kind != "inventory.scan" {
			t.Fatalf("reconnected stream did not receive the timed-out operation: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out delivered operation was not resent after reconnect")
	}

	secondCancel()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reconnected stream returned %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnected stream did not stop after cancellation")
	}
	select {
	case <-secondRecvDone:
	case <-time.After(time.Second):
		t.Fatal("reconnected stream receive loop did not stop after cancellation")
	}
}

func TestOperationStartedRecordsRunningLifecycle(t *testing.T) {
	database, server := gatewayTestServer(t, "operation-started-agent-token")
	ctx := context.Background()
	operationID, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]any{}, "operation-started")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkDelivered(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	delivered, err := database.Operation(ctx, operationID)
	if err != nil || delivered.DeliveredAt == nil {
		t.Fatalf("operation was not delivered: %#v %v", delivered, err)
	}
	time.Sleep(time.Millisecond)
	payload, err := json.Marshal(protocol.OperationStarted{OperationID: operationID})
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_started", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "running" || operation.StartedAt == nil || !operation.StartedAt.After(*delivered.DeliveredAt) {
		t.Fatalf("operation start was not recorded: %#v %v", operation, err)
	}
}

func TestFlushRetainsPostDeliveryCancellationProtectionForCodexTurns(t *testing.T) {
	database, server := gatewayTestServer(t, "flush-cancel-agent-token")
	ctx := context.Background()
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/flush-cancel", Name: "flush-cancel"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "flush cancellation")
	if err != nil {
		t.Fatal(err)
	}
	command := protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "cancel after delivery"}
	operationID, err := database.QueueOperation(ctx, server.ID, "codex.turn.start", command, "flush-cancel-turn")
	if err != nil {
		t.Fatal(err)
	}
	stream := &keepaliveStream{
		ctx:  ctx,
		sent: make(chan *protocol.ControlEnvelope, 2),
		onSend: func(message *protocol.ControlEnvelope) error {
			if message.OperationID != operationID {
				return nil
			}
			cancelled, err := database.CancelOperation(ctx, operationID, "cancelled during delivery")
			if err != nil {
				return err
			}
			if !cancelled {
				return errors.New("operation was not cancelled during delivery")
			}
			return nil
		},
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.flush(stream, server.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case delivered := <-stream.sent:
		if delivered.OperationID != operationID || delivered.Kind != "codex.turn.start" {
			t.Fatalf("unexpected delivered operation: %#v", delivered)
		}
	default:
		t.Fatal("Codex turn was not delivered")
	}
	operation, err := database.Operation(ctx, operationID)
	if err != nil || operation.Status != "cancelled" {
		t.Fatalf("cancelled operation was overwritten: %#v %v", operation, err)
	}
	pending, err := database.PendingOperations(ctx, server.ID)
	if err != nil || len(pending) != 1 || pending[0].Kind != "codex.turn.interrupt" {
		t.Fatalf("best-effort interrupt was not queued: %#v %v", pending, err)
	}
	var interrupt protocol.InterruptTurnCommand
	if err := json.Unmarshal([]byte(pending[0].Payload), &interrupt); err != nil {
		t.Fatal(err)
	}
	if interrupt.ThreadID != thread.ID || !interrupt.BestEffort {
		t.Fatalf("unexpected best-effort interrupt: %#v", interrupt)
	}
}

func gatewayTestServer(t *testing.T, agentToken string) (*store.Store, store.Server) {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	enrollmentToken := agentToken + "-enrollment"
	if _, err := database.CreateEnrollment(ctx, "queue-node", []string{"/srv"}, enrollmentToken, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, enrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "queue-node.local", agentToken)
	if err != nil {
		t.Fatal(err)
	}
	return database, server
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

func TestFailedCodexTurnUpdatesThreadAndPublishesFailure(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "codex-node", []string{"/srv"}, "codex-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "codex-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "codex-node.local", "codex-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "failed turn")
	if err != nil {
		t.Fatal(err)
	}
	command := protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: thread.WorkspaceID, Workspace: thread.Path, Prompt: "hello"}
	operationID, err := database.QueueOperation(ctx, server.ID, "codex.turn.start", command, "failed-codex-turn")
	if err != nil {
		t.Fatal(err)
	}
	result := protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "Codex turn/start: thread not found"}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.Thread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "failed" {
		t.Fatalf("unexpected thread status: %q", updated.Status)
	}
	events, err := database.Events(ctx, thread.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("unexpected thread events: %#v %v", events, err)
	}
	if events[0].Kind != "codex.turn.failed" || !strings.Contains(string(events[0].Payload), "thread not found") {
		t.Fatalf("unexpected failure event: %#v", events[0])
	}
	rewriteID, err := database.QueueOperation(ctx, server.ID, "codex.turn.rewrite", protocol.RewriteTurnCommand{Start: command, NumTurns: 1}, "failed-codex-rewrite")
	if err != nil {
		t.Fatal(err)
	}
	rewriteResult := protocol.OperationResult{OperationID: rewriteID, Status: "failed", Message: "rollback failed"}
	payload, err = json.Marshal(rewriteResult)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	events, err = database.Events(ctx, thread.ID, 0, 10)
	if err != nil || len(events) != 2 || !strings.Contains(string(events[1].Payload), "rollback failed") {
		t.Fatalf("rewrite failure was not published: %#v %v", events, err)
	}
}

func TestFailedInterruptWithMissingCodexThreadReconcilesThreadStatus(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "interrupt-node", []string{"/srv"}, "interrupt-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "interrupt-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "interrupt-node.local", "interrupt-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/interrupt", Name: "interrupt"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "stale interrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetThreadStatus(ctx, thread.ID, "running"); err != nil {
		t.Fatal(err)
	}
	command := protocol.InterruptTurnCommand{ThreadID: thread.ID, CodexThread: "codex-thread", TurnID: "turn-1"}
	operationID, err := database.QueueOperation(ctx, server.ID, "codex.turn.interrupt", command, "stale-interrupt-operation")
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	result := protocol.OperationResult{OperationID: operationID, Status: "failed", Message: "Codex turn/interrupt: thread not found: codex-thread (-32600)"}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.Thread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "interrupted" {
		t.Fatalf("stale interrupt did not reconcile thread status: %q", updated.Status)
	}
	events, err := database.Events(ctx, thread.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "codex.turn.cancelled" {
		t.Fatalf("unexpected stale interrupt events: %#v %v", events, err)
	}
}

func TestNoActiveCodexTurnRecognizesMissingTurnMessages(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{name: "no active turn", message: "Codex turn/interrupt: no active turn to interrupt (-32600)"},
		{name: "turn not found", message: "Codex turn/interrupt: turn not found (-32600)"},
		{name: "thread not found", message: "Codex turn/interrupt: thread not found: codex-thread (-32600)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !noActiveCodexTurn(test.message) {
				t.Fatalf("noActiveCodexTurn(%q) = false", test.message)
			}
		})
	}
}

func TestCancelledQueuedTurnIgnoresLateAgentEvents(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "cancel-node", []string{"/srv"}, "cancel-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "cancel-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "cancel-node.local", "cancel-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/cancel", Name: "cancel"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "cancelled turn")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ClaimThreadForTurn(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddEvent(ctx, protocol.StreamEvent{StreamID: thread.ID, Kind: "codex.turn.cancelled", Payload: json.RawMessage(`{"operation_id":"cancelled-operation","source":"control","text":"cancelled before Codex turn started"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetThreadStatus(ctx, thread.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	late := protocol.StreamEvent{StreamID: thread.ID, Kind: "turn.accepted", Payload: json.RawMessage(`{"codex_thread_id":"late-codex","turn_id":"late-turn"}`)}
	payload, err := json.Marshal(late)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.Thread(ctx, thread.ID)
	if err != nil || updated.Status != "idle" {
		t.Fatalf("late accepted event resurrected cancelled thread: %#v %v", updated, err)
	}
	events, err := database.Events(ctx, thread.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("late accepted event was persisted after cancellation: %#v %v", events, err)
	}
}

func TestCodexFirstResponseGeneratesTitleAndExplicitNameOverridesIt(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "codex-node", []string{"/srv"}, "title-enrollment-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "title-enrollment-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "codex-node.local", "title-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "")
	if err != nil {
		t.Fatal(err)
	}
	event := protocol.StreamEvent{EventID: "progress-response", StreamID: thread.ID, Kind: "codex.item.completed", Payload: json.RawMessage(`{"item":{"type":"agentMessage","text":"I will inspect the deployment flow first."}}`)}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	event = protocol.StreamEvent{EventID: "final-response", StreamID: thread.ID, Kind: "codex.item.completed", Payload: json.RawMessage(`{"item":{"type":"agentMessage","text":"## Fix deployment timeout\n\nThe upload no longer has a fixed deadline."}}`)}
	payload, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	event = protocol.StreamEvent{EventID: "first-turn-completed", StreamID: thread.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"status":"completed"}}`)}
	payload, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err := database.Thread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Fix deployment timeout" {
		t.Fatalf("unexpected generated title: %q", updated.Title)
	}
	event = protocol.StreamEvent{EventID: "later-response", StreamID: thread.ID, Kind: "codex.item.completed", Payload: json.RawMessage(`{"item":{"type":"agentMessage","text":"## This must not replace the first title"}}`)}
	payload, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	event = protocol.StreamEvent{EventID: "later-turn-completed", StreamID: thread.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"status":"completed"}}`)}
	payload, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err = database.Thread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Fix deployment timeout" {
		t.Fatalf("later turn replaced the generated title: %q", updated.Title)
	}
	event = protocol.StreamEvent{EventID: "explicit-title", StreamID: thread.ID, Kind: "codex.thread.name.updated", Payload: json.RawMessage(`{"threadId":"codex-thread","threadName":"  Deployment timeout  "}`)}
	payload, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updated, err = database.Thread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Deployment timeout" {
		t.Fatalf("explicit Codex title did not override generated title: %q", updated.Title)
	}
}

func TestUpsertApprovalStoresAndReopensReusedRequestID(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "approval-node", []string{"/srv"}, "approval-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "approval-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "approval-node.local", "approval-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	thread, err := database.CreateThread(ctx, workspaces[0].ID, "approval test")
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	event := protocol.StreamEvent{StreamID: thread.ID, Kind: "approval.requested", Payload: json.RawMessage(`{"request_id":"0","kind":"item/commandExecution/requestApproval","detail":{"command":"npm test","itemId":"call-old","turnId":"turn-old"}}`)}
	if err := gateway.upsertApproval(ctx, event); err != nil {
		t.Fatal(err)
	}
	var approval struct {
		RequestID string `db:"request_id"`
		Kind      string `db:"kind"`
		Detail    string `db:"detail"`
	}
	if err := database.DB.GetContext(ctx, &approval, "SELECT request_id,kind,detail FROM approvals WHERE thread_id=?", thread.ID); err != nil {
		t.Fatal(err)
	}
	if approval.RequestID != "0" || approval.Kind != "item/commandExecution/requestApproval" || !strings.Contains(approval.Detail, "npm test") {
		t.Fatalf("unexpected approval: %#v", approval)
	}
	completed := protocol.StreamEvent{EventID: "completed-event", StreamID: thread.ID, Kind: "codex.turn.completed", Payload: json.RawMessage(`{"turn":{"status":"interrupted"}}`)}
	payload, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	updatedThread, err := database.Thread(ctx, thread.ID)
	if err != nil || updatedThread.Status != "interrupted" {
		t.Fatalf("interrupted completion did not update thread status: %#v %v", updatedThread, err)
	}
	var resolved struct {
		Status   string `db:"status"`
		Decision string `db:"decision"`
	}
	if err := database.DB.GetContext(ctx, &resolved, "SELECT status,decision FROM approvals WHERE thread_id=?", thread.ID); err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "resolved" || resolved.Decision != "cancelled" {
		t.Fatalf("approval was not resolved with its turn: %#v", resolved)
	}
	cancelled := protocol.StreamEvent{EventID: "cancelled-event", StreamID: thread.ID, Kind: "codex.turn.cancelled", Payload: json.RawMessage(`{"turn":{"status":"cancelled"}}`)}
	cancelledPayload, err := json.Marshal(cancelled)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "event", PayloadJSON: cancelledPayload}); err != nil {
		t.Fatal(err)
	}
	updatedThread, err = database.Thread(ctx, thread.ID)
	if err != nil || updatedThread.Status != "interrupted" {
		t.Fatalf("cancelled turn did not release thread status: %#v %v", updatedThread, err)
	}

	reused := protocol.StreamEvent{StreamID: thread.ID, Kind: "approval.requested", Payload: json.RawMessage(`{"request_id":"0","kind":"item/commandExecution/requestApproval","detail":{"command":"ps aux","itemId":"call-new","turnId":"turn-new"}}`)}
	if err := gateway.upsertApproval(ctx, reused); err != nil {
		t.Fatal(err)
	}
	var reopened struct {
		Status     string         `db:"status"`
		Decision   sql.NullString `db:"decision"`
		ResolvedAt sql.NullTime   `db:"resolved_at"`
		Detail     string         `db:"detail"`
		Count      int            `db:"approval_count"`
	}
	if err := database.DB.GetContext(ctx, &reopened, `SELECT status,decision,resolved_at,detail,(SELECT COUNT(*) FROM approvals WHERE thread_id=?) AS approval_count FROM approvals WHERE thread_id=?`, thread.ID, thread.ID); err != nil {
		t.Fatal(err)
	}
	if reopened.Status != "pending" || reopened.Decision.Valid || reopened.ResolvedAt.Valid || reopened.Count != 1 || !strings.Contains(reopened.Detail, "call-new") {
		t.Fatalf("reused request id did not reopen approval: %#v", reopened)
	}

	if err := database.ResolvePendingApprovals(ctx, thread.ID, "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := gateway.upsertApproval(ctx, reused); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.GetContext(ctx, &resolved, "SELECT status,decision FROM approvals WHERE thread_id=?", thread.ID); err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "resolved" || resolved.Decision != "cancelled" {
		t.Fatalf("duplicate event reopened an already resolved approval: %#v", resolved)
	}
}

func TestCompletedTurnStatusUsesOfficialTerminalStatus(t *testing.T) {
	tests := []struct {
		payload string
		want    string
	}{
		{`{"turn":{"status":"completed"}}`, "idle"},
		{`{"turn":{"status":"interrupted"}}`, "interrupted"},
		{`{"turn":{"status":"cancelled"}}`, "interrupted"},
		{`{"turn":{"status":"failed","error":{"message":"provider disconnected"}}}`, "failed"},
		{`{"turn":{"status":"inProgress"}}`, "failed"},
		{`{}`, "failed"},
	}
	for _, test := range tests {
		if got := completedTurnStatus(json.RawMessage(test.payload)); got != test.want {
			t.Errorf("completedTurnStatus(%s) = %q, want %q", test.payload, got, test.want)
		}
	}
}

func TestWorkspaceFilesOperationStoresAgentSnapshot(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "wio.db") + "?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.CreateEnrollment(ctx, "files-node", []string{"/srv"}, "files-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	enrollment, err := database.ConsumeEnrollment(ctx, "files-token")
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.EnrollServer(ctx, enrollment, "files-node.local", "files-agent-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("unexpected workspaces: %#v %v", workspaces, err)
	}
	workspace := workspaces[0]
	operationID, err := database.QueueOperation(ctx, server.ID, "workspace.files", protocol.WorkspaceFilesCommand{WorkspaceID: workspace.ID, Path: workspace.Path}, "files-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BeginWorkspaceFileScan(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	resultData, err := json.Marshal(protocol.WorkspaceFilesResult{Files: []protocol.WorkspaceFile{{Path: "README.md", Kind: "file", Size: 42}}})
	if err != nil {
		t.Fatal(err)
	}
	resultPayload, err := json.Marshal(protocol.OperationResult{OperationID: operationID, Status: "succeeded", Data: resultData})
	if err != nil {
		t.Fatal(err)
	}
	gateway := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: resultPayload}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := database.WorkspaceFileSnapshot(ctx, workspace.ID)
	if err != nil || snapshot.Status != "succeeded" || !strings.Contains(snapshot.Files, "README.md") {
		t.Fatalf("unexpected workspace snapshot: %#v %v", snapshot, err)
	}
	previewID, err := database.QueueOperation(ctx, server.ID, "workspace.file.preview", protocol.WorkspaceFilePreviewCommand{WorkspaceID: workspace.ID, Root: workspace.Path, Path: "README.md"}, "preview-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BeginWorkspaceFilePreview(ctx, workspace.ID, "README.md"); err != nil {
		t.Fatal(err)
	}
	previewData, err := json.Marshal(protocol.WorkspaceFilePreviewResult{Path: "README.md", Content: "# Preview\n", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	previewPayload, err := json.Marshal(protocol.OperationResult{OperationID: previewID, Status: "succeeded", Data: previewData})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: previewPayload}); err != nil {
		t.Fatal(err)
	}
	preview, err := database.WorkspaceFilePreview(ctx, workspace.ID, "README.md")
	if err != nil || preview.Status != "succeeded" || preview.Content != "# Preview\n" {
		t.Fatalf("unexpected workspace preview: %#v %v", preview, err)
	}
	changesID, err := database.QueueOperation(ctx, server.ID, "workspace.changes", protocol.WorkspaceChangesCommand{WorkspaceID: workspace.ID, Path: workspace.Path}, "changes-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BeginWorkspaceChangeScan(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	changesData, err := json.Marshal(protocol.WorkspaceChangesResult{Changes: []protocol.WorkspaceChange{{Path: "README.md", Status: "modified", Unstaged: true}}})
	if err != nil {
		t.Fatal(err)
	}
	changesPayload, err := json.Marshal(protocol.OperationResult{OperationID: changesID, Status: "succeeded", Data: changesData})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: changesPayload}); err != nil {
		t.Fatal(err)
	}
	changesSnapshot, err := database.WorkspaceChangeSnapshot(ctx, workspace.ID)
	if err != nil || changesSnapshot.Status != "succeeded" || !strings.Contains(changesSnapshot.Changes, "README.md") {
		t.Fatalf("unexpected workspace changes: %#v %v", changesSnapshot, err)
	}
	diffID, err := database.QueueOperation(ctx, server.ID, "workspace.diff.preview", protocol.WorkspaceDiffCommand{WorkspaceID: workspace.ID, Root: workspace.Path, Path: "README.md"}, "diff-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BeginWorkspaceDiffPreview(ctx, workspace.ID, "README.md"); err != nil {
		t.Fatal(err)
	}
	diffData, err := json.Marshal(protocol.WorkspaceDiffResult{Path: "README.md", Content: "@@ -1 +1 @@\n-old\n+new\n", Additions: 1, Deletions: 1})
	if err != nil {
		t.Fatal(err)
	}
	diffPayload, err := json.Marshal(protocol.OperationResult{OperationID: diffID, Status: "succeeded", Data: diffData})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.handle(ctx, server.ID, &protocol.AgentEnvelope{Kind: "operation_result", PayloadJSON: diffPayload}); err != nil {
		t.Fatal(err)
	}
	diff, err := database.WorkspaceDiffPreview(ctx, workspace.ID, "README.md")
	if err != nil || diff.Status != "succeeded" || diff.Additions != 1 || diff.Deletions != 1 {
		t.Fatalf("unexpected workspace diff: %#v %v", diff, err)
	}
}
