package subagent

import (
	"encoding/json"

	"github.com/wio-platform/wio/internal/protocol"
)

// CodexUpdates also reads historical coordination events from older Agents.
func CodexUpdates(raw json.RawMessage) []protocol.SubagentUpdate {
	var payload struct {
		Item struct {
			Type, Tool, Status, Prompt, Model, Message string
			Receiver                                   string                                      `json:"receiverThreadId"`
			NewThread                                  string                                      `json:"newThreadId"`
			AgentThread                                string                                      `json:"agentThreadId"`
			AgentPath                                  string                                      `json:"agentPath"`
			Kind                                       string                                      `json:"kind"`
			Receivers                                  []string                                    `json:"receiverThreadIds"`
			State                                      json.RawMessage                             `json:"agentStatus"`
			States                                     map[string]struct{ Status, Message string } `json:"agentsStates"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	item := payload.Item
	if item.Type != "collabToolCall" && item.Type != "collabAgentToolCall" && item.Type != "subAgentActivity" {
		return nil
	}
	ids := append(item.Receivers, item.Receiver, item.NewThread, item.AgentThread)
	for id := range item.States {
		ids = append(ids, id)
	}
	var result []protocol.SubagentUpdate
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		state := item.States[id]
		status, message := state.Status, state.Message
		if status == "" && len(item.State) > 0 {
			var single struct{ Status, Message string }
			if json.Unmarshal(item.State, &single) == nil {
				status, message = single.Status, single.Message
			} else {
				_ = json.Unmarshal(item.State, &status)
			}
		}
		// Completion of spawn/send/wait is the coordination tool's state. Only
		// the explicitly reported child state proves that the child finished.
		if status == "" {
			status = "running"
		}
		if item.Type == "subAgentActivity" {
			status = item.Kind
			if status == "" {
				status = item.Status
			}
		}
		if item.Status == "failed" && state.Status == "" {
			status = "failed"
		}
		update := protocol.SubagentUpdate{ID: id, NativeID: id, Name: item.AgentPath, Status: Status(status), Prompt: item.Prompt, Model: item.Model, Detail: "result"}
		if message != "" {
			update.Entry = &protocol.SubagentEntry{ID: id + ":summary", Kind: "result", Text: message, Status: update.Status}
		}
		result = append(result, update)
	}
	return result
}

func CodexEntry(raw json.RawMessage) *protocol.SubagentEntry {
	var item struct {
		ID, Type, Text, Command, Status string
		Output                          string          `json:"aggregatedOutput"`
		Name                            string          `json:"tool"`
		Arguments                       json.RawMessage `json:"arguments"`
		Result                          json.RawMessage `json:"result"`
		Content                         json.RawMessage `json:"content"`
		ContentItems                    json.RawMessage `json:"contentItems"`
		Error                           json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return nil
	}
	entry := &protocol.SubagentEntry{ID: item.ID, Kind: "tool", Status: Status(item.Status)}
	switch item.Type {
	case "agentMessage":
		entry.Kind, entry.Text = "message", item.Text
	case "userMessage":
		entry.Kind, entry.Text = "user", Text(item.Content)
	case "plan":
		entry.Kind, entry.Text = "message", item.Text
	case "commandExecution":
		entry.Tool, entry.Text, entry.Output = "shell", item.Command, json.RawMessage("null")
		if item.Output != "" {
			entry.Output, _ = json.Marshal(item.Output)
		}
	case "mcpToolCall", "dynamicToolCall":
		entry.Tool, entry.Input, entry.Output = item.Name, item.Arguments, item.Result
		if len(item.ContentItems) > 0 && string(item.ContentItems) != "null" {
			entry.Output = item.ContentItems
		}
		if len(item.Error) > 0 && string(item.Error) != "null" {
			entry.Output = item.Error
		}
	case "fileChange":
		entry.Tool, entry.Output = "fileChange", raw
	case "webSearch":
		entry.Tool, entry.Output = "webSearch", raw
	default:
		return nil
	}
	return entry
}
