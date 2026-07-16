package codexadapter

import (
	"encoding/json"
	"testing"
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
