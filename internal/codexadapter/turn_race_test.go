package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

type recordedEvents struct {
	mu     sync.Mutex
	events []protocol.StreamEvent
}

func (r *recordedEvents) emit(event protocol.StreamEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordedEvents) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	kinds := make([]string, 0, len(r.events))
	for _, event := range r.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func raceTestAdapter(events *recordedEvents) *Adapter {
	return &Adapter{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		threads: map[string]string{"codex-thread": "wio-thread"},
		turns:   map[string]turnState{"wio-thread": {CodexThread: "codex-thread"}},
		approve: map[string]approvalRequest{},
		emit:    events.emit,
	}
}

func notify(adapter *Adapter, method, params string) {
	adapter.handleNotification(rpcMessage{Method: method, Params: json.RawMessage(params)})
}

func equalKinds(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// Reproduces the interleaving where readLoop handles turn/started and the
// terminal notification after delivering the turn/start response but before
// the requesting goroutine applies it.
func TestFastTurnCompletedBeforeStartResponseIsAppliedStaysCompleted(t *testing.T) {
	for _, terminal := range []string{"turn/completed", "turn/failed", "turn/cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			events := &recordedEvents{}
			adapter := raceTestAdapter(events)
			starts := 0
			request := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
				if method != "turn/start" {
					t.Fatalf("unexpected request %s", method)
				}
				starts++
				turnID := []string{"", "fast-turn", "next-turn"}[starts]
				notify(adapter, "turn/started", `{"threadId":"codex-thread","turn":{"id":"`+turnID+`"}}`)
				if starts == 1 {
					notify(adapter, terminal, `{"threadId":"codex-thread","turn":{"id":"`+turnID+`","status":"completed"}}`)
				}
				return json.RawMessage(`{"turn":{"id":"` + turnID + `"}}`), nil
			}
			command := protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "hi"}
			if err := adapter.startTurn(context.Background(), command, request); err != nil {
				t.Fatal(err)
			}
			state := adapter.turns["wio-thread"]
			if state.Active || state.Starting || state.TurnID != "fast-turn" || adapter.hasActiveTurn() {
				t.Fatalf("completed turn was resurrected: %#v", state)
			}
			terminalKind := "codex." + map[string]string{"turn/completed": "turn.completed", "turn/failed": "turn.failed", "turn/cancelled": "turn.cancelled"}[terminal]
			if got := events.kinds(); !equalKinds(got, "codex.turn.started", "turn.accepted", terminalKind) {
				t.Fatalf("terminal event must follow turn.accepted, got %v", got)
			}
			// A follow-up turn must be accepted and stay active until its own
			// terminal notification arrives.
			if err := adapter.startTurn(context.Background(), command, request); err != nil {
				t.Fatalf("follow-up turn refused: %v", err)
			}
			if state := adapter.turns["wio-thread"]; !state.Active || state.TurnID != "next-turn" {
				t.Fatalf("follow-up turn not active: %#v", state)
			}
		})
	}
}

func TestTerminalWithoutTurnIDBeforeStartResponseEndsStartingTurn(t *testing.T) {
	events := &recordedEvents{}
	adapter := raceTestAdapter(events)
	request := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		notify(adapter, "turn/completed", `{"threadId":"codex-thread"}`)
		return json.RawMessage(`{"turn":{"id":"fast-turn"}}`), nil
	}
	if err := adapter.startTurn(context.Background(), protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "hi"}, request); err != nil {
		t.Fatal(err)
	}
	if adapter.turns["wio-thread"].Active {
		t.Fatalf("turn stayed active: %#v", adapter.turns["wio-thread"])
	}
	if got := events.kinds(); !equalKinds(got, "turn.accepted", "codex.turn.completed") {
		t.Fatalf("unexpected event order %v", got)
	}
}

func TestDuplicateTerminalForPreviousTurnDoesNotEndStartingTurn(t *testing.T) {
	events := &recordedEvents{}
	adapter := raceTestAdapter(events)
	adapter.turns["wio-thread"] = turnState{CodexThread: "codex-thread", TurnID: "old-turn", FinishedTurnID: "old-turn"}
	request := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		notify(adapter, "turn/completed", `{"threadId":"codex-thread","turn":{"id":"old-turn","status":"completed"}}`)
		if !adapter.hasActiveTurn() {
			t.Error("duplicate terminal cleared the starting turn")
		}
		return json.RawMessage(`{"turn":{"id":"new-turn"}}`), nil
	}
	if err := adapter.startTurn(context.Background(), protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "hi"}, request); err != nil {
		t.Fatal(err)
	}
	if state := adapter.turns["wio-thread"]; !state.Active || state.TurnID != "new-turn" {
		t.Fatalf("new turn should be active: %#v", state)
	}
	if got := events.kinds(); !equalKinds(got, "codex.turn.completed", "turn.accepted") {
		t.Fatalf("unexpected events %v", got)
	}
}

func TestFailedTurnStartPublishesDeferredTerminalEvent(t *testing.T) {
	events := &recordedEvents{}
	adapter := raceTestAdapter(events)
	request := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		notify(adapter, "turn/failed", `{"threadId":"codex-thread","turn":{"id":"t1"}}`)
		return nil, errors.New("boom")
	}
	if err := adapter.startTurn(context.Background(), protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "hi"}, request); err == nil {
		t.Fatal("expected error")
	}
	if state := adapter.turns["wio-thread"]; state.Active || state.Starting || len(state.deferred) != 0 {
		t.Fatalf("failed start left pending state: %#v", state)
	}
	if got := events.kinds(); !equalKinds(got, "codex.turn.failed") {
		t.Fatalf("deferred event lost: %v", got)
	}
}

func TestResetDuringStartingTurnDoesNotResurrectIt(t *testing.T) {
	events := &recordedEvents{}
	adapter := raceTestAdapter(events)
	request := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		notify(adapter, "turn/completed", `{"threadId":"codex-thread","turn":{"id":"t1","status":"completed"}}`)
		adapter.reset(errors.New("app-server exited"))
		return json.RawMessage(`{"turn":{"id":"t1"}}`), nil
	}
	if err := adapter.startTurn(context.Background(), protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "hi"}, request); err == nil {
		t.Fatal("expected error after reset")
	}
	if adapter.hasActiveTurn() {
		t.Fatal("reset turn resurrected")
	}
	// The completed turn is published, and not additionally reported failed.
	if got := events.kinds(); !equalKinds(got, "codex.turn.completed") {
		t.Fatalf("unexpected events %v", got)
	}
}
