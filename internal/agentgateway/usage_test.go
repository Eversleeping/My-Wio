package agentgateway

import (
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/security"
)

func TestCodexUsagePayloadOnlyKeepsNumericTotals(t *testing.T) {
	raw := json.RawMessage(`{"threadId":"id","tokenUsage":{"total":{"inputTokens":1500,"outputTokens":250,"api_key":"secret"},"last":{"inputTokens":100},"access_token":"secret"},"token":"secret"}`)
	var got struct {
		Usage map[string]int64 `json:"usage"`
	}
	safe := security.RedactJSON(codexUsagePayload(raw))
	if err := json.Unmarshal(safe, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Usage) != 2 || got.Usage["input_tokens"] != 1500 || got.Usage["output_tokens"] != 250 {
		t.Fatalf("bad sanitized usage: %s", safe)
	}
	if string(codexUsagePayload(json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":"secret"}}}`))) != `{}` {
		t.Fatal("invalid usage should be discarded")
	}
}
