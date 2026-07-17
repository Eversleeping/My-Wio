package codexadapter

import (
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
}

func TestStartTurnParamsOmitDefaultReasoningEffort(t *testing.T) {
	params := turnStartParams(protocol.StartTurnCommand{Prompt: "test"}, "thread-123")
	if _, ok := params["effort"]; ok {
		t.Fatal("turn/start should omit an empty reasoning effort")
	}
}
