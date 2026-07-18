package agentgateway

import (
	"context"
	"database/sql"
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
}
