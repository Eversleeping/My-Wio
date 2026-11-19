package geminiadapter

import (
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestGeminiSubagentProgressDeduplicatesToolSnapshots(t *testing.T) {
	var events []protocol.StreamEvent
	c := geminiSubagents{stream: "parent", operation: "op", agents: map[string]protocol.SubagentUpdate{}, entries: map[string]string{}, emit: func(event protocol.StreamEvent) error { events = append(events, event); return nil }}
	progress := json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"delegate","_meta":{"wioSubagent":{"name":"reviewer","state":"running","prompt":{"task":"Review"},"recentActivity":[{"id":"read","type":"tool_call","content":"Read","args":"{}","status":"running"}]}}}}`)
	if err := c.update(progress); err != nil {
		t.Fatal(err)
	}
	count := len(events)
	if err := c.update(progress); err != nil || len(events) != count {
		t.Fatal("duplicate progress was appended")
	}
	if err := c.update(json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"delegate","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Review done"}}]}}`)); err != nil {
		t.Fatal(err)
	}
	if c.agents["delegate"].Status != "succeeded" {
		t.Fatalf("wrong status: %+v", c.agents)
	}
	var last protocol.SubagentUpdate
	if json.Unmarshal(events[len(events)-1].Payload, &last) != nil || last.Entry == nil || last.Entry.Text != "Review done" {
		t.Fatal("result missing")
	}
	cancelled := json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"cancelled","_meta":{"wioSubagent":{"name":"reviewer","state":"cancelled"}}}}`)
	if err := c.update(cancelled); err != nil {
		t.Fatal(err)
	}
	if err := c.update(json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"cancelled","status":"completed","content":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if c.agents["cancelled"].Status != "interrupted" {
		t.Fatal("wrapper completion overwrote native cancellation")
	}
}
