package agentgateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/realtime"
	"github.com/wio-platform/wio/internal/security"
	"github.com/wio-platform/wio/internal/session"
)

type sessionEnvelopeForm struct {
	event     func(string, protocol.StreamEvent) *protocol.AgentEnvelope
	ownership func(string, string, bool) *protocol.AgentEnvelope
}

func legacySessionEnvelopeForm() sessionEnvelopeForm {
	return sessionEnvelopeForm{
		event: func(engine string, event protocol.StreamEvent) *protocol.AgentEnvelope {
			kind := map[string]string{session.Codex: protocol.AgentKindEvent, session.Gemini: protocol.AgentKindGeminiEvent, session.Claude: protocol.AgentKindClaudeEvent}[engine]
			raw, _ := json.Marshal(event)
			return &protocol.AgentEnvelope{Kind: kind, PayloadJSON: raw}
		},
		ownership: func(engine, operation string, active bool) *protocol.AgentEnvelope {
			kind := protocol.AgentKindGeminiOwnershipLost
			if engine == session.Claude {
				kind = protocol.AgentKindClaudeOwnershipLost
			}
			if active {
				kind = protocol.AgentKindGeminiOwnershipActive
				if engine == session.Claude {
					kind = protocol.AgentKindClaudeOwnershipActive
				}
			}
			raw, _ := json.Marshal(protocol.GeminiOwnership{OperationID: operation})
			return &protocol.AgentEnvelope{Kind: kind, PayloadJSON: raw}
		},
	}
}

type envelopeScenarioResult struct {
	States []string
	Errors []string
	Events []string
}

// Both envelope forms will run this exact scenario after negotiation is added.
// The native engine rules, redaction, deduplication and ownership remain fixed.
func runSessionEnvelopeScenario(t *testing.T, engine string, form sessionEnvelopeForm) envelopeScenarioResult {
	t.Helper()
	ctx := context.Background()
	db, server := gatewayTestServer(t, "session-envelope-"+engine)
	if err := db.Heartbeat(ctx, server.ID, protocol.Heartbeat{AgentVersion: "0.3.26", Capabilities: testCapabilities("0.66.0", "2.1.318"), GeminiVersion: "0.66.0", ClaudeVersion: "2.1.318"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInventory(ctx, server.ID, protocol.Inventory{Repositories: []protocol.Repository{{Path: "/srv/project", Name: "project"}}}); err != nil {
		t.Fatal(err)
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces: %v %v", workspaces, err)
	}
	w := workspaces[0]
	var threadID, operationID string
	switch engine {
	case session.Codex:
		thread, e := db.CreateThread(ctx, w.ID, "envelope")
		if e != nil {
			t.Fatal(e)
		}
		threadID = thread.ID
		operationID, _, err = db.QueueCodexTurn(ctx, thread, protocol.StartTurnCommand{ThreadID: thread.ID, WorkspaceID: w.ID, Workspace: w.Path, Prompt: "hello", ApprovalMode: "on-request"})
	case session.Gemini:
		thread, e := db.CreateGeminiThread(ctx, w.ID, "auto", "default")
		if e != nil {
			t.Fatal(e)
		}
		threadID = thread.ID
		_, err = db.QueueGeminiTurn(ctx, thread, "hello", "auto", "default")
		current, e := db.GeminiThread(ctx, thread.ID)
		if e != nil {
			t.Fatal(e)
		}
		operationID = current.OperationID
	case session.Claude:
		thread, e := db.CreateClaudeThread(ctx, w.ID, "sonnet", "default", "")
		if e != nil {
			t.Fatal(e)
		}
		threadID = thread.ID
		_, err = db.QueueClaudeTurn(ctx, thread, "hello", "sonnet", "default", "")
		current, e := db.ClaudeThread(ctx, thread.ID)
		if e != nil {
			t.Fatal(e)
		}
		operationID = current.OperationID
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRunning(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	result := envelopeScenarioResult{}
	state := func() {
		var value string
		if err := db.DB.GetContext(ctx, &value, db.Q("SELECT status FROM "+engine+"_threads WHERE id=?"), threadID); err != nil {
			t.Fatal(err)
		}
		result.States = append(result.States, value)
	}
	send := func(id, kind, op string, owner string) error {
		payload, _ := json.Marshal(map[string]any{"operation_id": op, "session_id": "00000000-0000-4000-8000-000000000001", "text": "hello", "api_key": "fixture-secret", "item": map[string]any{"type": "agentMessage", "text": "hello"}})
		event := protocol.StreamEvent{EventID: id, StreamID: threadID, Kind: kind, OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Payload: payload}
		return g.handle(ctx, owner, form.event(engine, event))
	}
	initKind := map[string]string{session.Codex: protocol.EventCodexTurnStarted, session.Gemini: protocol.EventGeminiInit, session.Claude: protocol.EventClaudeInit}[engine]
	messageKind := map[string]string{session.Codex: protocol.EventCodexItemCompleted, session.Gemini: protocol.EventGeminiMessage, session.Claude: protocol.EventClaudeMessage}[engine]
	foreign := send("native-foreign", initKind, operationID, "foreign-server")
	if foreign == nil {
		t.Fatal("foreign event accepted")
	}
	result.Errors = append(result.Errors, foreign.Error())
	malformed := form.event(engine, protocol.StreamEvent{})
	malformed.PayloadJSON = json.RawMessage(`{`)
	if err := g.handle(ctx, server.ID, malformed); err == nil {
		t.Fatal("malformed event accepted")
	} else {
		result.Errors = append(result.Errors, err.Error())
	}
	if err := send("native-init", initKind, operationID, server.ID); err != nil {
		t.Fatal(err)
	}
	state()
	if engine != session.Codex {
		if err := send("native-stale", messageKind, "previous-operation", server.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := send("native-message", messageKind, operationID, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	if engine != session.Codex {
		if err := g.handle(ctx, "foreign-server", form.ownership(engine, operationID, false)); err == nil {
			t.Fatal("foreign ownership accepted")
		} else {
			result.Errors = append(result.Errors, err.Error())
		}
		for _, active := range []bool{false, true} {
			if err := g.handle(ctx, server.ID, form.ownership(engine, operationID, active)); err != nil {
				t.Fatal(err)
			}
			state()
		}
	}
	events, err := db.Events(ctx, threadID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	messageCount := 0
	for _, event := range events {
		if strings.HasPrefix(event.EventID, "native-") || strings.Contains(event.Kind, "ownership") {
			if event.EventID == "native-message" {
				messageCount++
			}
			if event.EventID == "native-stale" || event.EventID == "native-foreign" {
				t.Fatalf("rejected event persisted: %s", event.EventID)
			}
			normalized := strings.ReplaceAll(string(event.Payload), operationID, "<operation>")
			if strings.Contains(normalized, "fixture-secret") {
				t.Fatal("event was not redacted")
			}
			result.Events = append(result.Events, event.Kind+":"+normalized)
		}
	}
	if messageCount != 1 {
		t.Fatalf("native message deduplication changed: %d", messageCount)
	}
	expected := []string{"running"}
	if engine != session.Codex {
		expected = append(expected, "interrupted", "running")
	}
	if !reflect.DeepEqual(result.States, expected) {
		t.Fatalf("native states changed: %v", result.States)
	}
	return result
}

func TestLegacySessionEnvelopeScenarios(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) { runSessionEnvelopeScenario(t, engine, legacySessionEnvelopeForm()) })
	}
}

func negotiatedSessionEnvelopeForm() sessionEnvelopeForm {
	return sessionEnvelopeForm{
		event: func(engine string, event protocol.StreamEvent) *protocol.AgentEnvelope {
			raw, _ := json.Marshal(protocol.SessionEvent{Engine: engine, StreamEvent: event})
			return &protocol.AgentEnvelope{Kind: protocol.AgentKindSessionEvent, PayloadJSON: raw}
		},
		ownership: func(engine, operation string, active bool) *protocol.AgentEnvelope {
			raw, _ := json.Marshal(protocol.SessionOwnership{Engine: engine, OperationID: operation, Active: active})
			return &protocol.AgentEnvelope{Kind: protocol.AgentKindSessionOwnership, PayloadJSON: raw}
		},
	}
}
func TestNegotiatedSessionEnvelopesMatchLegacy(t *testing.T) {
	for _, engine := range []string{session.Codex, session.Gemini, session.Claude} {
		t.Run(engine, func(t *testing.T) {
			legacy := runSessionEnvelopeScenario(t, engine, legacySessionEnvelopeForm())
			negotiated := runSessionEnvelopeScenario(t, engine, negotiatedSessionEnvelopeForm())
			if !reflect.DeepEqual(legacy, negotiated) {
				t.Fatalf("envelope changed native behavior\nlegacy %+v\nnew %+v", legacy, negotiated)
			}
		})
	}
}
func TestSessionEnvelopesRejectUnknownEngines(t *testing.T) {
	db, server := gatewayTestServer(t, "unknown-session-engine")
	g := New(db, realtime.New(), security.DevVault(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, engine := range []string{"", "future-engine"} {
		event := negotiatedSessionEnvelopeForm().event(engine, protocol.StreamEvent{StreamID: "unknown", Payload: json.RawMessage(`{}`)})
		if err := g.handle(context.Background(), server.ID, event); err == nil || err.Error() != "unsupported session event engine" {
			t.Fatalf("unknown event engine accepted: %v", err)
		}
	}
	for _, engine := range []string{"", session.Codex, "future-engine"} {
		ownership := negotiatedSessionEnvelopeForm().ownership(engine, "unknown", true)
		if err := g.handle(context.Background(), server.ID, ownership); err == nil || err.Error() != "unsupported session ownership engine" {
			t.Fatalf("unknown ownership engine accepted: %v", err)
		}
	}
	stream := &keepaliveStream{ctx: context.Background(), sent: make(chan *protocol.ControlEnvelope, 1)}
	if err := g.sendKeepalive(stream, server.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	var keepalive protocol.GeminiKeepalive
	if err := json.Unmarshal((<-stream.sent).PayloadJSON, &keepalive); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keepalive.Capabilities, []string{protocol.ControlCapabilitySessionEnvelopes}) {
		t.Fatalf("missing receive capability: %v", keepalive.Capabilities)
	}
}
