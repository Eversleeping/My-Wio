package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/wio-platform/wio/internal/codexconfig"
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

func TestFailedApprovalWriteKeepsRequestPending(t *testing.T) {
	adapter := &Adapter{
		approve: map[string]approvalRequest{"request-1": {ID: json.RawMessage(`1`), Method: "item/commandExecution/requestApproval"}},
	}
	if err := adapter.Decide(protocol.ApprovalDecisionCommand{RequestID: "request-1", Decision: "approved"}); err == nil {
		t.Fatal("expected write failure without an app-server process")
	}
	if _, ok := adapter.approve["request-1"]; !ok {
		t.Fatal("failed write discarded the pending approval")
	}
}

func TestFindString(t *testing.T) {
	raw := json.RawMessage(`{"threadId":"thr_123"}`)
	if got := findString(raw, "threadId"); got != "thr_123" {
		t.Fatalf("unexpected thread id: %s", got)
	}
}

func TestCodexOperationReturnsUnsupportedForMissingMethod(t *testing.T) {
	a := &Adapter{}
	request := func(context.Context, string, any) (json.RawMessage, error) {
		return nil, &rpcRequestError{Method: "thread/goal/get", Code: -32601, Message: "method not found"}
	}
	payload := json.RawMessage(`{"codex_thread_id":"thread-1","codex_version":"codex-cli 0.139.0"}`)
	result, err := a.codexOperation(context.Background(), "codex.goal.get", payload, request)
	if err != nil || result.Supported || result.CodexVersion != "codex-cli 0.139.0" {
		t.Fatalf("unexpected result: %#v %v", result, err)
	}
}

func TestMCPResultDropsSchemasAndRawResources(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"name":"github","authStatus":"authenticated","tools":{"search":{"name":"search","inputSchema":{"secret":"no"}}},"resources":[{"uri":"secret"}],"resourceTemplates":[{"uriTemplate":"secret"}]}]}`)
	clean, err := sanitizeCodexResult("codex.mcp.list", raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(clean)
	if strings.Contains(text, "inputSchema") || strings.Contains(text, "uri") || !strings.Contains(text, `"tools":["search"]`) || !strings.Contains(text, `"resource_count":1`) || strings.Contains(text, `"data"`) {
		t.Fatalf("unexpected clean result: %s", text)
	}
}

func TestGoalResultIsDirectSnakeCaseDTO(t *testing.T) {
	raw := json.RawMessage(`{"goal":{"threadId":"thr","objective":"Ship","status":"active","tokenBudget":1000,"tokensUsed":25,"timeUsedSeconds":9,"createdAt":11,"updatedAt":12}}`)
	clean, err := sanitizeCodexResult("codex.goal.get", raw)
	if err != nil {
		t.Fatal(err)
	}
	var goal protocol.CodexGoal
	if err := json.Unmarshal(clean, &goal); err != nil {
		t.Fatal(err)
	}
	if goal.ThreadID != "thr" || goal.Objective != "Ship" || goal.TokenBudget == nil || *goal.TokenBudget != 1000 || strings.Contains(string(clean), "threadId") {
		t.Fatalf("unexpected goal: %s", clean)
	}
}

func TestSkillsResultFlattensGroupsToSnakeCaseDTOs(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"cwd":"/repo","errors":[],"skills":[{"name":"review","description":"Review code","path":"/skill","scope":"repo","enabled":true,"interface":{"displayName":"Reviewer","shortDescription":"Checks code"}}]}]}`)
	clean, err := sanitizeCodexResult("codex.skills.list", raw)
	if err != nil {
		t.Fatal(err)
	}
	var skills []protocol.CodexSkill
	if err := json.Unmarshal(clean, &skills); err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].DisplayName != "Reviewer" || !strings.Contains(string(clean), `"display_name":"Reviewer"`) || strings.Contains(string(clean), `"skills"`) {
		t.Fatalf("unexpected skills: %s", clean)
	}
}

func TestStatusResultProducesRateLimitArray(t *testing.T) {
	raw := json.RawMessage(`{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1700000000},"secondary":null},"rateLimitsByLimitId":null}`)
	clean, err := sanitizeCodexResult("codex.status.snapshot", raw)
	if err != nil {
		t.Fatal(err)
	}
	var status protocol.CodexStatusSnapshot
	if err := json.Unmarshal(clean, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.RateLimits) != 1 || status.RateLimits[0].UsedPercent == nil || *status.RateLimits[0].UsedPercent != 42 || status.RateLimits[0].ResetsAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("unexpected status: %s", clean)
	}
}

func TestStatusSnapshotSkipsChatGPTRateLimitsForAPIKeyAccount(t *testing.T) {
	var methods []string
	request := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		methods = append(methods, method)
		if method != "account/read" {
			t.Fatalf("unexpected method: %s", method)
		}
		return json.RawMessage(`{"account":{"type":"apiKey"},"requiresOpenaiAuth":false}`), nil
	}
	result, err := (&Adapter{}).codexOperation(context.Background(), "codex.status.snapshot", json.RawMessage(`{"codex_version":"codex-cli 0.145.0"}`), request)
	if err != nil {
		t.Fatal(err)
	}
	var status protocol.CodexStatusSnapshot
	if err := json.Unmarshal(result.Data, &status); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || status.AccountType != "apiKey" || status.RateLimitsAvailable || len(status.RateLimits) != 0 {
		t.Fatalf("unexpected API key status: methods=%#v status=%#v", methods, status)
	}
}

func TestStatusSnapshotReadsRateLimitsForChatGPTAccount(t *testing.T) {
	var methods []string
	request := func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "account/read":
			return json.RawMessage(`{"account":{"type":"chatgpt","email":"user@example.com","planType":"plus"},"requiresOpenaiAuth":true}`), nil
		case "account/rateLimits/read":
			return json.RawMessage(`{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":{"usedPercent":18,"resetsAt":1700000000}}}`), nil
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil, nil
		}
	}
	result, err := (&Adapter{}).codexOperation(context.Background(), "codex.status.snapshot", json.RawMessage(`{}`), request)
	if err != nil {
		t.Fatal(err)
	}
	var status protocol.CodexStatusSnapshot
	if err := json.Unmarshal(result.Data, &status); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[1] != "account/rateLimits/read" || status.AccountType != "chatgpt" || !status.RateLimitsAvailable || len(status.RateLimits) != 1 {
		t.Fatalf("unexpected ChatGPT status: methods=%#v status=%#v", methods, status)
	}
}

func TestCompactOperationUsesCurrentCodexThread(t *testing.T) {
	var method string
	var params map[string]string
	adapter := &Adapter{threads: make(map[string]string)}
	request := func(_ context.Context, requestedMethod string, raw any) (json.RawMessage, error) {
		method = requestedMethod
		params = raw.(map[string]string)
		return json.RawMessage(`{}`), nil
	}
	result, err := adapter.codexOperation(context.Background(), "codex.thread.compact", json.RawMessage(`{"thread_id":"wio-thread","codex_thread_id":"codex-thread","codex_version":"codex-cli 0.145.0"}`), request)
	if err != nil || !result.Supported || method != "thread/compact/start" || params["threadId"] != "codex-thread" {
		t.Fatalf("unexpected compact operation: method=%s params=%#v result=%#v err=%v", method, params, result, err)
	}
	if adapter.threads["codex-thread"] != "wio-thread" {
		t.Fatalf("compact operation did not bind notifications to the Wio thread: %#v", adapter.threads)
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
	if threadParams["sandbox"] != codexconfig.SandboxMode {
		t.Fatalf("unexpected thread/start sandbox: %#v", threadParams["sandbox"])
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

func TestPersistNotificationDropsIncrementalDeltas(t *testing.T) {
	for _, method := range []string{"item/agentMessage/delta", "item/commandExecution/outputDelta", "item/reasoning/summaryTextDelta"} {
		if persistNotification(method) {
			t.Errorf("incremental notification %q should not be persisted", method)
		}
	}
	for _, method := range []string{"item/completed", "turn/completed", "error"} {
		if !persistNotification(method) {
			t.Errorf("authoritative notification %q was dropped", method)
		}
	}
}

func TestRewriteTurnForksBeforeRollingBackAndStartingReplacement(t *testing.T) {
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
		if method == "thread/fork" {
			return json.RawMessage(`{"thread":{"id":"forked-thread"}}`), nil
		}
		if method == "thread/rollback" {
			rollbackParams = raw.(map[string]any)
			return json.RawMessage(`{"thread":{"id":"forked-thread"}}`), nil
		}
		return json.RawMessage(`{"turn":{"id":"replacement-turn"}}`), nil
	}
	command := protocol.RewriteTurnCommand{Start: protocol.StartTurnCommand{ThreadID: "wio-thread", CodexThread: "codex-thread", Prompt: "revised prompt"}, NumTurns: 2, EditEventID: "message-id", ReplacementEventID: "replacement-id", ReplacementPayload: json.RawMessage(`{"text":"revised prompt"}`), CutoffSequence: 12}
	if err := adapter.rewriteTurn(context.Background(), command, request); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 || methods[0] != "thread/fork" || methods[1] != "thread/rollback" || methods[2] != "turn/start" {
		t.Fatalf("unexpected rewrite request order: %#v", methods)
	}
	if rollbackParams["threadId"] != "forked-thread" || rollbackParams["numTurns"] != uint32(2) {
		t.Fatalf("unexpected rollback params: %#v", rollbackParams)
	}
	if emitted.Kind != "turn.accepted" || !strings.Contains(string(emitted.Payload), "replacement-turn") || !strings.Contains(string(emitted.Payload), "replacement-id") || !strings.Contains(string(emitted.Payload), "cutoff_sequence") {
		t.Fatalf("replacement turn was not accepted: %#v", emitted)
	}
}

func TestForkThreadUsesFixedAppServerMethod(t *testing.T) {
	adapter := &Adapter{threads: map[string]string{}, turns: map[string]turnState{}}
	var method string
	var params map[string]any
	result, err := adapter.forkThread(context.Background(), protocol.ForkThreadCommand{TargetThreadID: "wio-new", CodexThread: "codex-old", Workspace: "/srv/repo"}, func(_ context.Context, gotMethod string, raw any) (json.RawMessage, error) {
		method = gotMethod
		params = raw.(map[string]any)
		return json.RawMessage(`{"thread":{"id":"codex-new"}}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != "thread/fork" || params["threadId"] != "codex-old" || params["cwd"] != "/srv/repo" {
		t.Fatalf("unexpected fork request: %s %#v", method, params)
	}
	if result.CodexThread != "codex-new" || adapter.threads["codex-new"] != "wio-new" {
		t.Fatalf("unexpected fork result: %#v %#v", result, adapter.threads)
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
	if params["threadId"] != "codex-thread" || params["cwd"] != "/srv/project" || params["model"] != "gpt-5.6-sol" || params["sandbox"] != codexconfig.SandboxMode {
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
		if method == "turn/start" {
			return json.RawMessage(`{"turn":{"id":"replacement-turn"}}`), nil
		}
		return json.RawMessage(`{"thread":{"id":"replacement-thread"}}`), nil
	}
	if err := adapter.startTurn(context.Background(), command, request); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 || methods[0] != "thread/resume" || methods[1] != "thread/start" || methods[2] != "turn/start" {
		t.Fatalf("unexpected fallback methods=%#v", methods)
	}
	if emitted.StreamID != "wio-thread" || emitted.Kind != "turn.accepted" || !strings.Contains(string(emitted.Payload), "replacement-thread") {
		t.Fatalf("unexpected binding event: %#v", emitted)
	}
}

func TestUnknownThreadNotificationIsNeverGuessedFromSingleActiveTurn(t *testing.T) {
	var emitted []protocol.StreamEvent
	adapter := &Adapter{
		threads: map[string]string{"codex-a": "wio-a"},
		turns:   map[string]turnState{"wio-a": {CodexThread: "codex-a", TurnID: "turn-a", Active: true}},
		emit:    func(event protocol.StreamEvent) error { emitted = append(emitted, event); return nil },
	}
	adapter.handleNotification(rpcMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"codex-b","turn":{"id":"turn-b","status":"completed"}}`)})
	if len(emitted) != 0 || !adapter.turns["wio-a"].Active {
		t.Fatalf("unknown notification affected active thread: emitted=%#v state=%#v", emitted, adapter.turns["wio-a"])
	}
}

func TestStaleCompletionDoesNotFinishNewerTurn(t *testing.T) {
	var emitted []protocol.StreamEvent
	adapter := &Adapter{
		threads: map[string]string{"codex-thread": "wio-thread"},
		turns:   map[string]turnState{"wio-thread": {CodexThread: "codex-thread", TurnID: "new-turn", Active: true}},
		emit:    func(event protocol.StreamEvent) error { emitted = append(emitted, event); return nil },
	}
	adapter.handleNotification(rpcMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"codex-thread","turn":{"id":"old-turn","status":"completed"}}`)})
	if !adapter.turns["wio-thread"].Active || len(emitted) != 0 {
		t.Fatalf("stale completion affected the newer turn: state=%#v events=%#v", adapter.turns["wio-thread"], emitted)
	}
}

func TestResetEmitsFailureForEveryActiveTurn(t *testing.T) {
	var emitted []protocol.StreamEvent
	adapter := New("codex", slog.New(slog.NewTextHandler(io.Discard, nil)), func(event protocol.StreamEvent) error { emitted = append(emitted, event); return nil })
	adapter.turns["wio-a"] = turnState{CodexThread: "codex-a", TurnID: "turn-a", Active: true}
	adapter.turns["wio-b"] = turnState{CodexThread: "codex-b", TurnID: "turn-b", Active: true}
	adapter.reset(errors.New("app-server disconnected"))
	if len(emitted) != 2 {
		t.Fatalf("expected one failure per active turn, got %#v", emitted)
	}
	for _, event := range emitted {
		if event.Kind != "codex.turn.failed" || !strings.Contains(string(event.Payload), "app-server disconnected") {
			t.Fatalf("unexpected reset event: %#v", event)
		}
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

func TestReconfigureCommandRejectsActiveTurn(t *testing.T) {
	adapter := New("codex", slog.New(slog.NewTextHandler(io.Discard, nil)), func(protocol.StreamEvent) error { return nil })
	adapter.turns["wio-thread"] = turnState{CodexThread: "codex-thread", TurnID: "turn-id", Active: true}
	updated := false
	err := adapter.ReconfigureCommand("/managed/codex", func() error { updated = true; return nil })
	if err == nil || updated || adapter.command != "codex" {
		t.Fatalf("active turn did not block CLI update: command=%q updated=%v err=%v", adapter.command, updated, err)
	}
}
