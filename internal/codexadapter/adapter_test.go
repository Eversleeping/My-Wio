package codexadapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestApprovalDecisionCompatibility(t *testing.T) {
	if got := approvalDecision("item/commandExecution/requestApproval", "approved"); got != "accept" {
		t.Fatalf("unexpected V2 decision: %s", got)
	}
	if got := approvalDecision("execCommandApproval", "denied"); got != "denied" {
		t.Fatalf("unexpected legacy decision: %s", got)
	}
}

func TestFindString(t *testing.T) {
	raw := json.RawMessage(`{"threadId":"thr_123"}`)
	if got := findString(raw, "threadId"); got != "thr_123" {
		t.Fatalf("unexpected thread id: %s", got)
	}
}

func TestStartTurnParamsIncludeReasoningEffort(t *testing.T) {
	command := protocol.StartTurnCommand{
		Workspace:       "/srv/project",
		Prompt:          "test prompt",
		Images:          []protocol.TurnImage{{DataURL: "data:image/png;base64,Zm9v"}},
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "xhigh",
		ApprovalMode:    "on-request",
	}
	threadParams := threadStartParams(command)
	if _, ok := threadParams["effort"]; ok {
		t.Fatal("thread/start must not include the turn-only effort field")
	}
	turnParams := turnStartParams(command, "thread-123")
	if turnParams["effort"] != "xhigh" {
		t.Fatalf("unexpected turn/start effort: %#v", turnParams["effort"])
	}
	if turnParams["model"] != "gpt-5.6-sol" {
		t.Fatalf("unexpected turn/start model: %#v", turnParams["model"])
	}
	input, ok := turnParams["input"].([]map[string]string)
	if !ok || len(input) != 2 || input[1]["type"] != "image" || input[1]["url"] != command.Images[0].DataURL {
		t.Fatalf("unexpected turn/start input: %#v", turnParams["input"])
	}
}

func TestStartTurnParamsOmitDefaultReasoningEffort(t *testing.T) {
	params := turnStartParams(protocol.StartTurnCommand{Prompt: "test"}, "thread-123")
	if _, ok := params["effort"]; ok {
		t.Fatal("turn/start should omit an empty reasoning effort")
	}
}

func TestRewriteTurnRollsBackBeforeStartingReplacement(t *testing.T) {
	var methods []string
	var rollbackParams map[string]any
	var emitted protocol.StreamEvent
	adapter := &Adapter{
		threads: map[string]string{"codex-thread": "wio-thread"},
		turns:   map[string]turnState{"wio-thread": {CodexThread: "codex-thread"}},
		emit: func(event protocol.StreamEvent) error {
			emitted = event
			return nil
		},
	}
	request := func(_ context.Context, method string, raw any) (json.RawMessage, error) {
		methods = append(methods, method)
		if method == "thread/rollback" {
			rollbackParams = raw.(map[string]any)
			return json.RawMessage(`{"thread":{"id":"codex-thread"}}`), nil
		}
		return json.RawMessage(`{"turn":{"id":"replacement-turn"}}`), nil
	}
	command := protocol.RewriteTurnCommand{Start: protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "revised prompt"}, NumTurns: 2}
	if err := adapter.rewriteTurn(context.Background(), command, request); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "thread/rollback" || methods[1] != "turn/start" {
		t.Fatalf("unexpected rewrite request order: %#v", methods)
	}
	if rollbackParams["threadId"] != "codex-thread" || rollbackParams["numTurns"] != uint32(2) {
		t.Fatalf("unexpected rollback params: %#v", rollbackParams)
	}
	if emitted.Kind != "turn.accepted" || !strings.Contains(string(emitted.Payload), "replacement-turn") {
		t.Fatalf("replacement turn was not accepted: %#v", emitted)
	}
}

func TestInterruptUsesCapturedTurnInsteadOfLatestAdapterState(t *testing.T) {
	adapter := &Adapter{turns: map[string]turnState{"wio-thread": {CodexThread: "codex-thread", TurnID: "new-turn"}}}
	var params map[string]string
	request := func(_ context.Context, method string, raw any) (json.RawMessage, error) {
		if method != "turn/interrupt" {
			t.Fatalf("unexpected method: %s", method)
		}
		params = raw.(map[string]string)
		return json.RawMessage(`{}`), nil
	}
	command := protocol.InterruptTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", TurnID: "old-turn"}
	if err := adapter.interrupt(context.Background(), command, request); err != nil {
		t.Fatal(err)
	}
	if params["turnId"] != "old-turn" {
		t.Fatalf("interrupt targeted the wrong turn: %#v", params)
	}
}

func TestPrepareCodexThreadResumesAfterAdapterRestart(t *testing.T) {
	adapter := &Adapter{threads: make(map[string]string), turns: make(map[string]turnState)}
	command := protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Workspace: "/srv/project", Model: "gpt-5.6-sol", ApprovalMode: "on-request"}
	var method string
	var params map[string]any
	request := func(_ context.Context, requestedMethod string, requestedParams any) (json.RawMessage, error) {
		method = requestedMethod
		params = requestedParams.(map[string]any)
		return json.RawMessage(`{"thread":{"id":"codex-thread"}}`), nil
	}
	threadID, err := adapter.prepareCodexThread(context.Background(), command, request)
	if err != nil {
		t.Fatal(err)
	}
	if threadID != "codex-thread" || method != "thread/resume" {
		t.Fatalf("unexpected resume result: thread=%q method=%q", threadID, method)
	}
	if params["threadId"] != "codex-thread" || params["cwd"] != "/srv/project" || params["model"] != "gpt-5.6-sol" {
		t.Fatalf("unexpected thread/resume params: %#v", params)
	}
}

func TestPrepareCodexThreadRebindsWhenStoredThreadIsMissing(t *testing.T) {
	var emitted protocol.StreamEvent
	adapter := &Adapter{
		threads: make(map[string]string),
		turns:   make(map[string]turnState),
		emit: func(event protocol.StreamEvent) error {
			emitted = event
			return nil
		},
	}
	command := protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "missing-thread", Workspace: "/srv/project"}
	var methods []string
	request := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		methods = append(methods, method)
		if method == "thread/resume" {
			return nil, &rpcRequestError{Method: method, Code: -32600, Message: "thread not found: missing-thread"}
		}
		return json.RawMessage(`{"thread":{"id":"replacement-thread"}}`), nil
	}
	threadID, err := adapter.prepareCodexThread(context.Background(), command, request)
	if err != nil {
		t.Fatal(err)
	}
	if threadID != "replacement-thread" || len(methods) != 2 || methods[0] != "thread/resume" || methods[1] != "thread/start" {
		t.Fatalf("unexpected fallback: thread=%q methods=%#v", threadID, methods)
	}
	if emitted.StreamID != "wio-thread" || emitted.Kind != "thread.bound" || !json.Valid(emitted.Payload) {
		t.Fatalf("unexpected binding event: %#v", emitted)
	}
}

func TestPrepareCodexThreadDoesNotRebindOnOtherResumeErrors(t *testing.T) {
	adapter := &Adapter{threads: make(map[string]string), turns: make(map[string]turnState)}
	command := protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread"}
	var methods []string
	request := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		methods = append(methods, method)
		return nil, &rpcRequestError{Method: method, Code: -32000, Message: "authentication failed"}
	}
	if _, err := adapter.prepareCodexThread(context.Background(), command, request); err == nil {
		t.Fatal("expected resume error")
	}
	if len(methods) != 1 || methods[0] != "thread/resume" {
		t.Fatalf("unexpected fallback after non-missing error: %#v", methods)
	}
}

func TestResetForcesPersistedThreadResume(t *testing.T) {
	adapter := &Adapter{threads: map[string]string{"codex-thread": "wio-thread"}, turns: map[string]turnState{"wio-thread": {CodexThread: "codex-thread"}}, pending: make(map[string]chan rpcMessage), approve: make(map[string]approvalRequest)}
	adapter.reset(nil)
	if len(adapter.threads) != 0 || len(adapter.turns) != 0 {
		t.Fatalf("adapter retained stale thread state: threads=%#v turns=%#v", adapter.threads, adapter.turns)
	}
}

func TestReconfigureEnvironmentRejectsActiveTurn(t *testing.T) {
	adapter := NewWithEnvironment("codex", []string{"OLD=value"}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(protocol.StreamEvent) error { return nil })
	adapter.turns["wio-thread"] = turnState{CodexThread: "codex-thread", TurnID: "turn-id", Active: true}
	updated := false
	err := adapter.ReconfigureEnvironment([]string{"NEW=value"}, func() error { updated = true; return nil })
	if err == nil || updated {
		t.Fatalf("active turn did not block reconfiguration: updated=%v err=%v", updated, err)
	}
}

func TestCompletedTurnAllowsEnvironmentReconfiguration(t *testing.T) {
	adapter := NewWithEnvironment("codex", []string{"OLD=value"}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(protocol.StreamEvent) error { return nil })
	adapter.threads["codex-thread"] = "wio-thread"
	adapter.turns["wio-thread"] = turnState{CodexThread: "codex-thread", TurnID: "turn-id", Active: true}
	adapter.handleNotification(rpcMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"codex-thread"}`)})
	updated := false
	if err := adapter.ReconfigureEnvironment([]string{"NEW=value"}, func() error { updated = true; return nil }); err != nil || !updated || len(adapter.environment) != 1 || adapter.environment[0] != "NEW=value" {
		t.Fatalf("completed turn did not allow reconfiguration: updated=%v environment=%#v err=%v", updated, adapter.environment, err)
	}
}
