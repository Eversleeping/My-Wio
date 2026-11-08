package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// flushEvents waits until every queued event has been handed to emit.
func flushEvents(t *testing.T, a *Adapter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	// hasPendingEvents takes the queue lock, which orders the caller's reads
	// after the last delivery.
	for a.hasPendingEvents() {
		if time.Now().After(deadline) {
			t.Fatal("queued Codex events were not delivered")
		}
		time.Sleep(time.Millisecond)
	}
}

// blockingSink stands in for the Agent's emit while its outbound queue is
// full: every call blocks until released.
type blockingSink struct {
	mu      sync.Mutex
	events  []protocol.StreamEvent
	entered chan string
	release chan struct{}
	fail    atomic.Int32
}

func newBlockingSink() *blockingSink {
	return &blockingSink{entered: make(chan string, 64), release: make(chan struct{})}
}

func (s *blockingSink) emit(event protocol.StreamEvent) error {
	select {
	case s.entered <- event.Kind:
	default:
	}
	<-s.release
	if s.fail.Load() > 0 {
		s.fail.Add(-1)
		return errors.New("agent outbound queue is full")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *blockingSink) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := make([]string, 0, len(s.events))
	for _, event := range s.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func queueTestAdapter(emit EmitFunc) *Adapter {
	a := New("", slog.New(slog.NewTextHandler(io.Discard, nil)), emit)
	a.threads["codex-thread"] = "wio-thread"
	a.turns["wio-thread"] = turnState{CodexThread: "codex-thread", TurnID: "turn-1", Active: true}
	return a
}

func TestReadLoopKeepsServingResponsesWhileEmitBlocks(t *testing.T) {
	sink := newBlockingSink()
	a := queueTestAdapter(sink.emit)
	response := make(chan rpcMessage, 1)
	a.pending["7"] = response
	input := strings.Join([]string{
		`{"method":"item/completed","params":{"threadId":"codex-thread","turnId":"turn-1","item":{"id":"a","type":"agentMessage","text":"one"}}}`,
		`{"id":42,"method":"item/commandExecution/requestApproval","params":{"threadId":"codex-thread","turnId":"turn-1","itemId":"cmd"}}`,
		`{"method":"turn/completed","params":{"threadId":"codex-thread","turn":{"id":"turn-1","status":"completed"}}}`,
		`{"id":7,"result":{"ok":true}}`,
	}, "\n") + "\n"
	done := make(chan error, 1)
	go func() { done <- a.readLoop(strings.NewReader(input)) }()
	if kind := <-sink.entered; kind != "codex.item.completed" {
		t.Fatalf("unexpected first event %s", kind)
	}
	// emit is blocked on the first event; the reader must still deliver the
	// RPC response that follows the notifications.
	select {
	case <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("readLoop stalled behind a blocked emit")
	}
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("read loop: %v", err)
	}
	if a.hasActiveTurn() {
		t.Fatal("terminal notification was not applied while emit was blocked")
	}
	close(sink.release)
	flushEvents(t, a)
	if got := sink.kinds(); !equalKinds(got, "codex.item.completed", "approval.requested", "codex.turn.completed") {
		t.Fatalf("events lost or reordered: %v", got)
	}
}

func TestFailedDeliveryIsRetriedInOrder(t *testing.T) {
	sink := newBlockingSink()
	close(sink.release)
	sink.fail.Store(2)
	a := queueTestAdapter(sink.emit)
	notify(a, "item/completed", `{"threadId":"codex-thread","turnId":"turn-1","item":{"id":"a"}}`)
	a.handleServerRequest(rpcMessage{ID: json.RawMessage(`5`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"codex-thread","turnId":"turn-1"}`)})
	notify(a, "turn/completed", `{"threadId":"codex-thread","turn":{"id":"turn-1","status":"completed"}}`)
	flushEvents(t, a)
	if got := sink.kinds(); !equalKinds(got, "codex.item.completed", "approval.requested", "codex.turn.completed") {
		t.Fatalf("failed deliveries were dropped or reordered: %v", got)
	}
}

func TestBackloggedQueueDropsOnlyProgressEvents(t *testing.T) {
	sink := newBlockingSink()
	a := queueTestAdapter(sink.emit)
	notify(a, "item/completed", `{"threadId":"codex-thread","turnId":"turn-1","item":{"id":"head"}}`)
	<-sink.entered
	for i := 0; i < maxQueuedEvents+10; i++ {
		notify(a, "item/completed", fmt.Sprintf(`{"threadId":"codex-thread","turnId":"turn-1","item":{"id":"%d"}}`, i))
	}
	a.handleServerRequest(rpcMessage{ID: json.RawMessage(`9`), Method: protocol.UserInputRequestMethod, Params: json.RawMessage(`{"threadId":"codex-thread","turnId":"turn-1","questions":[]}`)})
	notify(a, "turn/completed", `{"threadId":"codex-thread","turn":{"id":"turn-1","status":"completed"}}`)
	a.events.mu.Lock()
	queued, dropped := len(a.events.pending), a.events.dropped
	a.events.mu.Unlock()
	// The head is still queued while emit is blocked on it.
	if queued != maxQueuedEvents+2 || dropped != 11 {
		t.Fatalf("backlog bound not applied: queued=%d dropped=%d", queued, dropped)
	}
	close(sink.release)
	flushEvents(t, a)
	got := sink.kinds()
	if len(got) != maxQueuedEvents+2 || got[len(got)-2] != "approval.requested" || got[len(got)-1] != "codex.turn.completed" {
		t.Fatalf("approval or terminal event was dropped: %d events, tail %v", len(got), got[len(got)-2:])
	}
}

func TestAcceptedTurnDoesNotFailWhenEmitFails(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var kinds []string
	a := &Adapter{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		threads: map[string]string{},
		turns:   map[string]turnState{},
		approve: map[string]approvalRequest{},
		emit: func(event protocol.StreamEvent) error {
			if calls.Add(1) == 1 {
				return errors.New("agent outbound queue is full")
			}
			mu.Lock()
			defer mu.Unlock()
			kinds = append(kinds, event.Kind)
			return nil
		},
	}
	request := func(context.Context, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
	}
	if err := a.startPreparedTurn(context.Background(), protocol.StartTurnCommand{ThreadID: "wio-thread", Prompt: "hi"}, "codex-thread", nil, request); err != nil {
		t.Fatalf("a turn Codex accepted was reported failed: %v", err)
	}
	if !a.hasActiveTurn() {
		t.Fatal("accepted turn is not active")
	}
	flushEvents(t, a)
	mu.Lock()
	defer mu.Unlock()
	if !equalKinds(kinds, "turn.accepted") {
		t.Fatalf("turn.accepted was not retried: %v", kinds)
	}
}

func TestCloseStopsRetryingAndEndsDelivery(t *testing.T) {
	previous := closeDrainTimeout
	closeDrainTimeout = 100 * time.Millisecond
	defer func() { closeDrainTimeout = previous }()
	var attempts atomic.Int32
	a := queueTestAdapter(func(protocol.StreamEvent) error {
		attempts.Add(1)
		return errors.New("agent outbound queue is full")
	})
	notify(a, "turn/completed", `{"threadId":"codex-thread","turn":{"id":"turn-1","status":"completed"}}`)
	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("failed delivery was not retried")
		}
		time.Sleep(time.Millisecond)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// Without Close the terminal event would be retried forever; after it the
	// delivery goroutine must give up and exit, also for later events.
	flushEvents(t, a)
	a.publish(protocol.StreamEvent{StreamID: "wio-thread", Kind: "codex.turn.failed", Payload: json.RawMessage(`{}`)})
	flushEvents(t, a)
}

func TestSessionReaperKeepsUndeliveredEvents(t *testing.T) {
	p, _ := sessionTestPool(t)
	sink := newBlockingSink()
	a, release, err := p.acquire("pending-events")
	if err != nil {
		t.Fatal(err)
	}
	release()
	a.emit = sink.emit
	a.publish(protocol.StreamEvent{StreamID: "pending-events", Kind: "codex.turn.completed", Payload: json.RawMessage(`{}`)})
	<-sink.entered
	p.mu.Lock()
	p.sessions["pending-events"].lastUsed = time.Now().Add(-2 * sessionIdleTimeout)
	p.mu.Unlock()
	p.reapIdle(time.Now())
	p.mu.Lock()
	kept := p.sessions["pending-events"] != nil
	p.mu.Unlock()
	if !kept {
		t.Fatal("a session with an undelivered terminal event was reclaimed")
	}
	close(sink.release)
	flushEvents(t, a)
	p.reapIdle(time.Now())
	p.mu.Lock()
	kept = p.sessions["pending-events"] != nil
	p.mu.Unlock()
	if kept {
		t.Fatal("idle session was not reclaimed after its events were delivered")
	}
}
