package claudeadapter

import (
	"encoding/json"
	"testing"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeSubagentFramesIncludeNestedContentAndBackgroundCompletion(t *testing.T) {
	var events []protocol.StreamEvent
	c := claudeSubagents{stream: "parent", operation: "op", agents: map[string]protocol.SubagentUpdate{}, tasks: map[string]string{}, emit: func(event protocol.StreamEvent) error { events = append(events, event); return nil }}
	frames := []string{
		`{"type":"assistant","uuid":"a","message":{"content":[{"type":"tool_use","id":"spawn","name":"Agent","input":{"description":"Review","prompt":"Review code"}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"native","tool_use_id":"spawn","task_type":"local_agent"}`,
		`{"type":"assistant","uuid":"b","parent_tool_use_id":"spawn","message":{"content":[{"type":"text","text":"Reading sources"},{"type":"tool_use","id":"read","name":"Read","input":{"file_path":"a.go"}}]}}`,
		`{"type":"user","uuid":"c","parent_tool_use_id":"spawn","message":{"content":[{"type":"tool_result","tool_use_id":"read","content":"source"}]}}`,
		`{"type":"user","uuid":"d","tool_use_result":{"status":"async_launched","agentId":"native"},"message":{"content":[{"type":"tool_result","tool_use_id":"spawn","content":"running in background"}]}}`,
		`{"type":"assistant","uuid":"e","parent_tool_use_id":"spawn","message":{"content":[{"type":"tool_use","id":"nested","name":"Task","input":{"description":"Test"}}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"native","tool_use_id":"spawn","status":"completed","summary":"Review done","uuid":"final"}`,
	}
	for index, raw := range frames {
		if err := c.frame([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if index == 4 && c.agents["spawn"].Status != "running" {
			t.Fatal("background launch marked completed")
		}
	}
	if c.agents["spawn"].Status != "succeeded" || c.agents["nested"].ParentID != "spawn" {
		t.Fatalf("wrong state %+v", c.agents)
	}
	text := false
	toolResult := false
	for _, event := range events {
		if event.StreamID != "parent" || event.Kind != protocol.EventSessionSubagentUpdated {
			t.Fatal("escaped parent stream")
		}
		var update protocol.SubagentUpdate
		_ = json.Unmarshal(event.Payload, &update)
		if update.Entry != nil {
			text = text || update.Entry.Text == "Reading sources"
			toolResult = toolResult || update.Entry.ID == "read" && len(update.Entry.Output) > 0
		}
	}
	if !text || !toolResult {
		t.Fatalf("child content missing: %v", events)
	}
	if err := c.frame([]byte(`{"type":"system","subtype":"task_started","task_id":"resumed","task_type":"local_agent","description":"Resumed review"}`)); err != nil {
		t.Fatal(err)
	}
	if c.agents["resumed"].Status != "running" {
		t.Fatal("resumed native child was not observed")
	}
	if err := c.finish(false); err != nil {
		t.Fatal(err)
	}
	if c.agents["resumed"].Status != "unknown" || c.agents["spawn"].Status != "succeeded" {
		t.Fatal("process exit did not preserve confirmed and uncertain child results")
	}
}
