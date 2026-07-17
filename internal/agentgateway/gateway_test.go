package agentgateway

import (
	"context"
	"encoding/json"
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
}

func TestUpsertApprovalStoresSnakeCaseRequestID(t *testing.T) {
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
	event := protocol.StreamEvent{StreamID: thread.ID, Kind: "approval.requested", Payload: json.RawMessage(`{"request_id":"request-123","kind":"item/commandExecution/requestApproval","detail":{"command":"npm test"}}`)}
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
	if approval.RequestID != "request-123" || approval.Kind != "item/commandExecution/requestApproval" || !strings.Contains(approval.Detail, "npm test") {
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
}
