package subagent

import (
	"encoding/json"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

// ToolUpdate preserves task/result records collected before native child
// observation existed. Tool results only match IDs discovered as agent calls.
func ToolUpdate(engine, kind string, raw json.RawMessage, known func(string) bool) []protocol.SubagentUpdate {
	var data struct {
		ID, Name, Status string
		ToolID           string                                      `json:"tool_id"`
		ToolName         string                                      `json:"tool_name"`
		Input            struct{ Description, Prompt, Model string } `json:"input"`
		Content          json.RawMessage
		Output           json.RawMessage
		Error            bool `json:"is_error"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	update := protocol.SubagentUpdate{Detail: "result"}
	if engine == "claude" {
		update.ID = data.ID
		if kind == protocol.EventClaudeToolUse {
			if data.Name != "Agent" && data.Name != "Task" {
				return nil
			}
			update.Name, update.Prompt, update.Model, update.Status = data.Input.Description, data.Input.Prompt, data.Input.Model, "running"
		} else {
			if !known(update.ID) {
				return nil
			}
			update.Status = "succeeded"
			if data.Error {
				update.Status = "failed"
			}
			text := Text(data.Content)
			if strings.Contains(text, "running in the background") || strings.Contains(text, "async_launched") {
				update.Status = "running"
			}
			update.Entry = &protocol.SubagentEntry{ID: update.ID + ":summary", Kind: "result", Text: text}
		}
	} else {
		update.ID = data.ToolID
		if kind == protocol.EventGeminiToolUse {
			if !strings.HasPrefix(data.ToolName, "Running subagent '") && !strings.HasPrefix(data.ToolName, "Delegating to agent '") {
				return nil
			}
			update.Name, update.Status = data.ToolName, Status(data.Status)
		} else {
			if !known(update.ID) {
				return nil
			}
			update.Status = Status(data.Status)
			update.Entry = &protocol.SubagentEntry{ID: update.ID + ":summary", Kind: "result", Text: Text(data.Output)}
		}
	}
	return []protocol.SubagentUpdate{update}
}
