package codexadapter

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestCodexChildLifecycleDoesNotCompleteParent(t *testing.T) {
	var mu sync.Mutex
	var events []protocol.StreamEvent
	reply := make(chan struct{}, 1)
	a := New("codex", slog.New(slog.NewTextHandler(io.Discard, nil)), func(event protocol.StreamEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		var update protocol.SubagentUpdate
		if json.Unmarshal(event.Payload, &update) == nil && update.Entry != nil && update.Entry.Text == "Child reply" {
			reply <- struct{}{}
		}
		return nil
	})
	a.threads["native-parent"] = "wio-parent"
	a.turns["wio-parent"] = turnState{CodexThread: "native-parent", TurnID: "parent-turn", Active: true}
	a.handleNotification(rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"native-parent","item":{"type":"collabAgentToolCall","tool":"spawnAgent","status":"completed","receiverThreadIds":["native-child"],"agentsStates":{"native-child":{"status":"running"}}}}`)})
	a.handleNotification(rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"native-child","turnId":"child-turn","item":{"id":"message","type":"agentMessage","text":"Child reply"}}`)})
	a.handleNotification(rpcMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"native-child","turn":{"id":"child-turn","status":"completed"}}`)})
	if !a.turns["wio-parent"].Active {
		t.Fatal("child completion ended parent turn")
	}
	if a.subagents["native-child"].Info.Status != "succeeded" {
		t.Fatal("child did not complete")
	}
	select {
	case <-reply:
	case <-time.After(2 * time.Second):
		t.Fatal("child reply was not delivered")
	}
	a.Close()
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, event := range events {
		if event.Kind == protocol.EventCodexTurnCompleted {
			t.Fatal("child terminal event escaped into parent conversation")
		}
		var update protocol.SubagentUpdate
		if event.Kind == protocol.EventSessionSubagentUpdated && json.Unmarshal(event.Payload, &update) == nil && update.Entry != nil && update.Entry.Text == "Child reply" {
			found = true
		}
	}
	if !found {
		t.Fatal("child reply lost")
	}
}
