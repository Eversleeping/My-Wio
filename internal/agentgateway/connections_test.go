package agentgateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/store"
)

func TestConnectionRunPreservesContextErrorsDuringAuthentication(t *testing.T) {
	for _, phase := range []string{"already canceled", "during authentication", "deadline during authentication"} {
		t.Run(phase, func(t *testing.T) {
			const token = "cancel-authentication-token"
			database, server := gatewayTestServer(t, token)
			ctx, cancel := context.WithCancel(context.Background())
			if phase == "deadline during authentication" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			}
			defer cancel()
			// Hold the only pool connection so AuthenticateAgent waits inside
			// database/sql until the stream is cancelled or its deadline expires.
			database.DB.SetMaxOpenConns(1)
			held, err := database.DB.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			slot := &serverConnections{}
			connection := &gatewayConnection{ctx: ctx, cancel: cancel, slot: slot, gateway: &Gateway{store: database}, serverID: server.ID, token: token}
			slot.current.Store(connection)
			if phase == "already canceled" {
				cancel()
			}
			waits := database.DB.Stats().WaitCount
			done := make(chan error, 1)
			actionRan := false
			go func() {
				done <- connection.run(func() error { actionRan = true; return nil })
			}()
			if phase != "already canceled" {
				deadline := time.NewTimer(time.Second)
				defer deadline.Stop()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for database.DB.Stats().WaitCount == waits {
					select {
					case <-ticker.C:
					case <-deadline.C:
						t.Fatal("authentication did not wait for the database connection")
					}
				}
				if phase == "during authentication" {
					cancel()
				}
			}
			want := context.Canceled
			if phase == "deadline during authentication" {
				want = context.DeadlineExceeded
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("run returned %v, want %v", err, want)
				}
				if actionRan {
					t.Fatal("action ran after authentication was cancelled")
				}
			case <-time.After(time.Second):
				t.Fatal("run did not stop after context cancellation")
			}
		})
	}
}

func TestConnectionRunPreservesTemporaryStorageErrorsWithoutInvalidatingToken(t *testing.T) {
	const token = "storage-error-token"
	database, server := gatewayTestServer(t, token)
	unavailable, _ := gatewayTestServer(t, "unavailable-store-token")
	if err := unavailable.Close(); err != nil {
		t.Fatal(err)
	}
	workingDB := database.DB
	database.DB = unavailable.DB
	defer func() { database.DB = workingDB }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slot := &serverConnections{}
	connection := &gatewayConnection{ctx: ctx, cancel: cancel, slot: slot, gateway: &Gateway{store: database}, serverID: server.ID, token: token}
	slot.current.Store(connection)
	// An unavailable database must not turn a valid credential into an
	// Unauthenticated error or cancel the stream's context in run.
	_, storageErr := database.AuthenticateAgent(ctx, token)
	if storageErr == nil {
		t.Fatal("closed database unexpectedly authenticated the Agent")
	}
	actionRan := false
	err := connection.run(func() error { actionRan = true; return nil })
	if !errors.Is(err, storageErr) {
		t.Fatalf("run returned %v, want original storage error %v", err, storageErr)
	}
	if ctx.Err() != nil || actionRan {
		t.Fatalf("storage failure invalidated the context or ran the action: context=%v action=%v", ctx.Err(), actionRan)
	}
	// Restore storage availability and retry with the same token and context.
	database.DB = workingDB
	if err := connection.run(func() error { actionRan = true; return nil }); err != nil || !actionRan {
		t.Fatalf("authentication did not recover after temporary storage failure: action=%v error=%v", actionRan, err)
	}
}

func TestConnectNewestStreamSupersedesOldWithoutMarkingOffline(t *testing.T) {
	database, server := gatewayTestServer(t, "multiple-streams-token")
	g := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.keepaliveEvery, g.pollEvery = time.Hour, time.Hour
	start := func() (*keepaliveStream, context.CancelFunc, <-chan error) {
		t.Helper()
		ctx, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer multiple-streams-token")))
		s := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 8)}
		done := make(chan error, 1)
		go func() { done <- g.Connect(s) }()
		select {
		case <-s.sent:
		case <-time.After(time.Second):
			t.Fatal("no initial keepalive")
		}
		return s, cancel, done
	}
	old, oldCancel, oldDone := start()
	defer oldCancel()
	newest, newestCancel, newestDone := start()
	defer newestCancel()
	if err := database.Heartbeat(context.Background(), server.ID, protocol.Heartbeat{}); err != nil {
		t.Fatal(err)
	}
	// No cancellation from the client is needed: opening a replacement closes
	// the superseded Connect even when its Recv is waiting forever.
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("superseded Connect stayed open")
	}
	var currentStatus string
	if err := database.DB.Get(&currentStatus, database.Q("SELECT status FROM servers WHERE id=?"), server.ID); err != nil {
		t.Fatal(err)
	}
	if currentStatus != "online" {
		t.Fatalf("old stream cleanup marked live server %q", currentStatus)
	}
	id, err := database.QueueOperation(context.Background(), server.ID, "inventory.scan", map[string]any{}, "newest-stream-only")
	if err != nil {
		t.Fatal(err)
	}
	g.Wake(server.ID)
	select {
	case message := <-newest.sent:
		if message.OperationID != id {
			t.Fatalf("unexpected operation: %+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("newest stream did not receive operation")
	}
	select {
	case message := <-old.sent:
		t.Fatalf("old stream received operation: %+v", message)
	default:
	}
	newestCancel()
	select {
	case <-newestDone:
	case <-time.After(time.Second):
		t.Fatal("newest stream did not close")
	}
}

func TestConnectRejectsRotatedTokenBeforeDelivery(t *testing.T) {
	database, server := gatewayTestServer(t, "rotating-stream-token")
	g := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.keepaliveEvery, g.pollEvery = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer rotating-stream-token")))
	defer cancel()
	s := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 8)}
	done := make(chan error, 1)
	go func() { done <- g.Connect(s) }()
	select {
	case <-s.sent:
	case <-time.After(time.Second):
		t.Fatal("no initial keepalive")
	}
	if _, err := database.DB.Exec(database.Q("UPDATE agent_credentials SET token_hash=? WHERE server_id=?"), store.HashToken("new-token"), server.ID); err != nil {
		t.Fatal(err)
	}
	id, err := database.QueueOperation(context.Background(), server.ID, "inventory.scan", map[string]any{}, "rotated-token-operation")
	if err != nil {
		t.Fatal(err)
	}
	g.Wake(server.ID)
	select {
	case err := <-done:
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("unexpected close reason: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("old token stream did not close")
	}
	operation, err := database.Operation(context.Background(), id)
	if err != nil || operation.Status != "queued" {
		t.Fatalf("old token delivered work: %+v %v", operation, err)
	}
}

func TestFlushFailsUndecryptableOperationAndContinues(t *testing.T) {
	database, server := gatewayTestServer(t, "bad-payload-token")
	ctx := context.Background()
	bad, err := database.QueueOperation(ctx, server.ID, "credentials.configure", map[string]any{}, "bad-ciphertext")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.Exec(database.Q("UPDATE agent_operations SET payload=? WHERE id=?"), "v1:invalid-ciphertext", bad); err != nil {
		t.Fatal(err)
	}
	good, err := database.QueueOperation(ctx, server.ID, "inventory.scan", map[string]any{}, "after-bad-ciphertext")
	if err != nil {
		t.Fatal(err)
	}
	g := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &keepaliveStream{ctx: ctx, sent: make(chan *protocol.ControlEnvelope, 4)}
	if err := g.flush(s, server.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := database.Operation(ctx, bad)
	if err != nil || failed.Status != "failed" || failed.CompletedAt == nil || !strings.Contains(failed.Result, "decrypt") {
		t.Fatalf("bad operation did not finish explicitly: %+v %v", failed, err)
	}
	select {
	case message := <-s.sent:
		if message.OperationID != good {
			t.Fatalf("bad operation sent to Agent: %+v", message)
		}
	default:
		t.Fatal("subsequent operation was not delivered")
	}
	// A second poll must leave the terminal result alone.
	if err := g.flush(s, server.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.DB.Get(&count, database.Q("SELECT COUNT(*) FROM events WHERE stream_id=? AND kind='operation.failed'"), server.ID); err != nil || count != 1 {
		t.Fatalf("expected one failure event, got %d: %v", count, err)
	}
}

func TestConnectReplacementUnblocksStalledSend(t *testing.T) {
	database, _ := gatewayTestServer(t, "blocked-stream-token")
	g := New(database, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.keepaliveEvery, g.pollEvery = time.Hour, time.Hour
	streamContext := func() (context.Context, context.CancelFunc) {
		return context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer blocked-stream-token")))
	}
	oldCtx, oldCancel := streamContext()
	defer oldCancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	old := &keepaliveStream{ctx: oldCtx, sent: make(chan *protocol.ControlEnvelope, 4), onSend: func(*protocol.ControlEnvelope) error {
		close(entered)
		<-release
		return nil
	}}
	oldDone := make(chan error, 1)
	go func() { oldDone <- g.Connect(old) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old stream did not enter Send")
	}
	newCtx, newCancel := streamContext()
	defer newCancel()
	newest := &keepaliveStream{ctx: newCtx, sent: make(chan *protocol.ControlEnvelope, 4)}
	newDone := make(chan error, 1)
	go func() { newDone <- g.Connect(newest) }()
	select {
	case <-newest.sent:
	case <-time.After(time.Second):
		t.Fatal("replacement waited for blocked Send")
	}
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old Connect did not return during blocked Send")
	}
	newCancel()
	select {
	case <-newDone:
	case <-time.After(time.Second):
		t.Fatal("new Connect did not close")
	}
}
