package codexadapter

import (
	"context"
	"encoding/json"
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
