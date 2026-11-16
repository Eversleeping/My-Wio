package agent

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
)

func advertisedSessionEnvelopes() *controlPlaneCapabilities {
	capabilities := &controlPlaneCapabilities{}
	raw, _ := json.Marshal(protocol.GeminiKeepalive{Capabilities: []string{protocol.ControlCapabilitySessionEnvelopes}})
	capabilities.observeKeepalive(raw)
	return capabilities
}
func TestControlPlaneCapabilityDeclarationIsConnectionScoped(t *testing.T) {
	capabilities := &controlPlaneCapabilities{}
	for _, raw := range []string{`{}`, `{"capabilities":["future.capability"]}`, `{`} {
		capabilities.observeKeepalive(json.RawMessage(raw))
		if capabilities.sessionEnvelopes.Load() {
			t.Fatal("missing/unknown declaration enabled new envelopes")
		}
	}
	capabilities = advertisedSessionEnvelopes()
	if !capabilities.sessionEnvelopes.Load() {
		t.Fatal("declaration was ignored")
	}
	capabilities.observeKeepalive(json.RawMessage(`{`))
	if !capabilities.sessionEnvelopes.Load() {
		t.Fatal("malformed traffic erased an observed declaration")
	}
	fresh := &controlPlaneCapabilities{}
	if fresh.sessionEnvelopes.Load() {
		t.Fatal("new connection inherited declaration")
	}
	capabilities.observeKeepalive(json.RawMessage(`{}`))
	if capabilities.sessionEnvelopes.Load() {
		t.Fatal("absent declaration failed to restore legacy format")
	}
}

func queueSessionEnvelopeSamples(t *testing.T, client *Client) []*protocol.AgentEnvelope {
	t.Helper()
	samples := []*protocol.AgentEnvelope{}
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		event := protocol.StreamEvent{EventID: "event-" + engine, StreamID: "thread-" + engine, Sequence: 42, Kind: map[string]string{session.Codex: protocol.EventCodexItemCompleted, session.Gemini: protocol.EventGeminiMessage, session.Claude: protocol.EventClaudeMessage}[engine], OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Payload: json.RawMessage(`{"text":"unchanged","usage":{"tokens":3}}`)}
		if err := client.emitSessionEvent(engine, event); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, <-client.outbound)
	}
	for _, kind := range []string{protocol.AgentKindGeminiOwnershipLost, protocol.AgentKindGeminiOwnershipActive, protocol.AgentKindClaudeOwnershipLost, protocol.AgentKindClaudeOwnershipActive} {
		if err := client.queue(kind, protocol.GeminiOwnership{OperationID: "operation"}, true); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, <-client.outbound)
	}
	for _, sample := range samples {
		client.outbound <- sample
	}
	return samples
}
func jsonFields(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
func TestSendLoopNegotiatesAllSessionEnvelopeKinds(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-control-plane", true: "declared-control-plane"}[enabled], func(t *testing.T) {
			client := outboundTestClient(16)
			originals := queueSessionEnvelopeSamples(t, client)
			capabilities := &controlPlaneCapabilities{}
			if enabled {
				capabilities = advertisedSessionEnvelopes()
			}
			stream := &fakeAgentStream{}
			if err := runSendLoopUntil(t, client, stream, func() bool { return len(stream.kinds()) == len(originals) }, capabilities); err != nil {
				t.Fatal(err)
			}
			for i, original := range originals {
				actual := stream.sent[i]
				if actual.MessageID != original.MessageID || actual.ServerID != original.ServerID || actual.OccurredAtUnixMS != original.OccurredAtUnixMS {
					t.Fatal("session translation changed envelope identity")
				}
				if !enabled {
					if !reflect.DeepEqual(actual, original) {
						t.Fatal("legacy format changed")
					}
					continue
				}
				legacy := legacySessionEnvelopes[original.Kind]
				expected := jsonFields(t, original.PayloadJSON)
				expected["engine"] = legacy.engine
				kind := protocol.AgentKindSessionEvent
				if legacy.ownership {
					kind = protocol.AgentKindSessionOwnership
					expected["active"] = legacy.active
				}
				if actual.Kind != kind || !reflect.DeepEqual(expected, jsonFields(t, actual.PayloadJSON)) {
					t.Fatalf("translated payload changed: %+v", actual)
				}
				if original.Kind == kind || jsonFields(t, original.PayloadJSON)["engine"] != nil {
					t.Fatal("queue entry was mutated")
				}
			}
		})
	}
}
func TestSendLoopObservesDeclarationWhileConnected(t *testing.T) {
	client := outboundTestClient(4)
	originals := queueSessionEnvelopeSamples(t, outboundTestClient(16))
	client.outbound <- originals[0]
	stream := &fakeAgentStream{}
	capabilities := &controlPlaneCapabilities{}
	changed := false
	if err := runSendLoopUntil(t, client, stream, func() bool {
		count := len(stream.kinds())
		if count == 1 && !changed {
			changed = true
			capabilities.observeKeepalive(json.RawMessage(`{"capabilities":["session.envelopes"]}`))
			client.outbound <- originals[1]
		}
		return count == 2
	}, capabilities); err != nil {
		t.Fatal(err)
	}
	if kinds := stream.kinds(); !reflect.DeepEqual(kinds, []string{protocol.AgentKindEvent, protocol.AgentKindSessionEvent}) {
		t.Fatalf("declaration did not change subsequent sends: %v", kinds)
	}
}
func TestSessionRetryRenegotiatesAfterControlPlaneRollback(t *testing.T) {
	client := outboundTestClient(16)
	originals := queueSessionEnvelopeSamples(t, client)
	attemptKind := ""
	broken := &fakeAgentStream{fail: func(envelope *protocol.AgentEnvelope) error { attemptKind = envelope.Kind; return io.EOF }}
	if err := runSendLoopUntil(t, client, broken, func() bool { return false }, advertisedSessionEnvelopes()); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if attemptKind != protocol.AgentKindSessionEvent {
		t.Fatal("new connection did not use negotiated format")
	}
	client.sentMu.Lock()
	retry := client.retry
	client.sentMu.Unlock()
	if retry != originals[0] {
		t.Fatal("retry saved the translated envelope")
	}
	recovered := &fakeAgentStream{}
	if err := runSendLoopUntil(t, client, recovered, func() bool { return len(recovered.kinds()) == len(originals) }, &controlPlaneCapabilities{}); err != nil {
		t.Fatal(err)
	}
	for i, original := range originals {
		if !reflect.DeepEqual(recovered.sent[i], original) {
			t.Fatalf("rollback changed/reordered message %d", i)
		}
	}
}
func TestSessionEnvelopeTranslationPreservesUnknownFieldsAndSizeFallback(t *testing.T) {
	original := &protocol.AgentEnvelope{MessageID: "message", ServerID: "server", Kind: protocol.AgentKindGeminiEvent, PayloadJSON: json.RawMessage(`{"event_id":"event","future_optional":{"kept":true}}`)}
	translated := negotiatedSessionEnvelope(original)
	if translated.Kind != protocol.AgentKindSessionEvent || !reflect.DeepEqual(jsonFields(t, original.PayloadJSON)["future_optional"], jsonFields(t, translated.PayloadJSON)["future_optional"]) {
		t.Fatal("unknown optional content was dropped")
	}
	for _, raw := range []string{`null`, `{`, `[]`} {
		malformed := *original
		malformed.PayloadJSON = json.RawMessage(raw)
		if negotiatedSessionEnvelope(&malformed) != &malformed {
			t.Fatal("unconvertible payload changed")
		}
	}
	for _, kind := range []string{protocol.AgentKindHeartbeat, protocol.AgentKindOperationResult, "future_kind"} {
		other := *original
		other.Kind = kind
		if negotiatedSessionEnvelope(&other) != &other {
			t.Fatal("non-session envelope changed")
		}
	}
	edge := *original
	padding := maxAgentMessageBytes - agentEnvelopeOverhead - len(edge.MessageID) - len(edge.ServerID) - len(edge.Kind) - len(`{"padding":""}`)
	edge.PayloadJSON = json.RawMessage(`{"padding":"` + strings.Repeat("x", padding) + `"}`)
	if envelopeTooLarge(&edge) {
		t.Fatal("invalid legacy boundary sample")
	}
	if negotiatedSessionEnvelope(&edge) != &edge {
		t.Fatal("new format lost a message that fits the old size limit")
	}
}
