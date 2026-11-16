package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

func TestSessionRunnersRejectMalformedControlsBeforeAdapters(t *testing.T) {
	client := &Client{}
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		runner := client.sessionRunner(engine)
		if runner == nil {
			t.Fatalf("missing runner %s", engine)
		}
		calls := map[string]func(context.Context, *protocol.ControlEnvelope) error{
			"start": runner.Start, "steer": runner.Steer, "answer": runner.Answer,
			"interrupt": func(ctx context.Context, envelope *protocol.ControlEnvelope) error {
				data, err := runner.Interrupt(ctx, envelope)
				if len(data) != 0 {
					t.Fatal("malformed interruption returned data")
				}
				return err
			},
		}
		for action, call := range calls {
			t.Run(engine+"/"+action, func(t *testing.T) {
				err := call(context.Background(), &protocol.ControlEnvelope{OperationID: "malformed", PayloadJSON: json.RawMessage(`{`)})
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) {
					t.Fatalf("native adapter reached before decoding: %v", err)
				}
			})
		}
	}
	if client.sessionRunner("unknown") != nil {
		t.Fatal("unknown engine selected a native runner")
	}
}

func TestSessionEventEntryPreservesLegacyWireContract(t *testing.T) {
	client := &Client{outbound: make(chan *protocol.AgentEnvelope, 1)}
	kinds := map[string]string{session.Codex: protocol.AgentKindEvent, session.Gemini: protocol.AgentKindGeminiEvent, session.Claude: protocol.AgentKindClaudeEvent}
	ids := map[string]bool{}
	for engine, kind := range kinds {
		for _, providedID := range []string{"stable-id", ""} {
			original := protocol.StreamEvent{EventID: providedID, StreamID: "session", Sequence: 42, Kind: "native.event", OccurredAt: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC), Payload: json.RawMessage(`{"text":"unchanged","usage":{"tokens":3}}`)}
			if err := client.emitSessionEvent(engine, original); err != nil {
				t.Fatal(err)
			}
			envelope := receiveAgentEnvelope(t, client.outbound)
			if envelope.Kind != kind {
				t.Fatalf("%s envelope=%q want %q", engine, envelope.Kind, kind)
			}
			var decoded protocol.StreamEvent
			if err := json.Unmarshal(envelope.PayloadJSON, &decoded); err != nil {
				t.Fatal(err)
			}
			if providedID == "" {
				if decoded.EventID == "" || ids[decoded.EventID] {
					t.Fatal("missing/duplicate generated event ID")
				}
				ids[decoded.EventID] = true
				original.EventID = decoded.EventID
			}
			if !reflect.DeepEqual(decoded, original) {
				t.Fatalf("event changed: %+v want %+v", decoded, original)
			}
		}
	}
	if err := client.emitSessionEvent("unknown", protocol.StreamEvent{}); err == nil || len(client.outbound) != 0 {
		t.Fatal("unknown engine published into a native stream")
	}
}
