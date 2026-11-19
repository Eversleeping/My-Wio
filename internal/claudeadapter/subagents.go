package claudeadapter

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"reflect"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/subagent"
)

type claudeSubagents struct {
	stream, operation string
	emit              func(protocol.StreamEvent) error
	agents            map[string]protocol.SubagentUpdate
	tasks             map[string]string
	questions         map[string]bool
}

func (c *claudeSubagents) update(update protocol.SubagentUpdate) error {
	previous := c.agents[update.ID]
	if update.ParentID != "" {
		previous.ParentID = update.ParentID
	}
	if update.NativeID != "" {
		previous.NativeID = update.NativeID
	}
	if update.Name != "" {
		previous.Name = update.Name
	}
	if update.Status != "" {
		previous.Status = update.Status
	}
	if update.Prompt != "" {
		previous.Prompt = update.Prompt
	}
	if update.Model != "" {
		previous.Model = update.Model
	}
	if update.Detail != "" {
		previous.Detail = update.Detail
	}
	previous.ID, previous.OperationID = update.ID, c.operation
	if !reflect.DeepEqual(previous, c.agents[update.ID]) {
		c.agents[update.ID] = previous
		if err := c.emit(subagent.Event(c.stream, previous)); err != nil {
			return err
		}
	}
	if update.Entry != nil {
		event := subagent.Event(c.stream, protocol.SubagentUpdate{ID: update.ID, OperationID: c.operation, Entry: update.Entry})
		event.EventID = uuid.NewSHA1(uuid.NameSpaceOID, append([]byte(c.operation), event.Payload...)).String()
		return c.emit(event)
	}
	return nil
}

func (c *claudeSubagents) frame(raw []byte) error {
	var frame struct {
		Type, Subtype, UUID, Status, Description, Summary, Prompt string
		Parent                                                    *string `json:"parent_tool_use_id"`
		ToolID                                                    string  `json:"tool_use_id"`
		TaskID                                                    string  `json:"task_id"`
		TaskType                                                  string  `json:"task_type"`
		AgentType                                                 string  `json:"subagent_type"`
		TaskParent                                                string  `json:"parent_task_id"`
		Result                                                    struct {
			Status  string `json:"status"`
			AgentID string `json:"agentId"`
		} `json:"tool_use_result"`
		Message struct {
			Content json.RawMessage `json:"content"`
			Model   string          `json:"model"`
		} `json:"message"`
		Patch struct{ Status, Description string } `json:"patch"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return nil
	}
	if frame.UUID == "" {
		frame.UUID = uuid.NewSHA1(uuid.NameSpaceOID, raw).String()
	}
	parent := ""
	if frame.Parent != nil && !strings.HasPrefix(*frame.Parent, "forked-command-") {
		parent = *frame.Parent
	}
	if frame.Type == "system" && strings.HasPrefix(frame.Subtype, "task_") {
		id := c.tasks[frame.TaskID]
		if frame.ToolID != "" {
			id = frame.ToolID
		}
		if id == "" && (frame.TaskType == "local_agent" || frame.AgentType != "") {
			id = frame.TaskID
		}
		if id == "" || (c.agents[id].ID == "" && frame.TaskType != "local_agent" && frame.AgentType == "") {
			return nil
		}
		c.tasks[frame.TaskID] = id
		status := "running"
		if frame.Subtype == "task_notification" {
			status = subagent.Status(frame.Status)
		}
		if frame.Subtype == "task_updated" {
			status = ""
			if frame.Patch.Status != "" {
				status = subagent.Status(frame.Patch.Status)
			}
		}
		update := protocol.SubagentUpdate{ID: id, NativeID: frame.TaskID, ParentID: c.tasks[frame.TaskParent], Name: frame.Description, Prompt: frame.Prompt, Status: status}
		if frame.Patch.Description != "" {
			update.Name = frame.Patch.Description
		}
		if frame.Summary != "" {
			update.Entry = &protocol.SubagentEntry{ID: frame.UUID, Kind: "message", Text: frame.Summary}
		}
		return c.update(update)
	}
	if parent != "" && c.agents[parent].ID == "" {
		if err := c.update(protocol.SubagentUpdate{ID: parent, Name: "Claude", Status: "running", Detail: "activity"}); err != nil {
			return err
		}
	}
	if parent != "" && frame.Message.Model != "" {
		if err := c.update(protocol.SubagentUpdate{ID: parent, Model: frame.Message.Model}); err != nil {
			return err
		}
	}
	var blocks []struct {
		Type, ID, Name, Text string
		Input                json.RawMessage
		ToolID               string `json:"tool_use_id"`
		Content              json.RawMessage
		Error                bool `json:"is_error"`
	}
	_ = json.Unmarshal(frame.Message.Content, &blocks)
	for index, block := range blocks {
		if frame.Type == "assistant" && block.Type == "tool_use" && (block.Name == "Agent" || block.Name == "Task") {
			var input struct{ Description, Prompt, Model string }
			_ = json.Unmarshal(block.Input, &input)
			if err := c.update(protocol.SubagentUpdate{ID: block.ID, ParentID: parent, Name: input.Description, Prompt: input.Prompt, Model: input.Model, Status: "running", Detail: "activity"}); err != nil {
				return err
			}
		}
		if frame.Type == "user" && block.Type == "tool_result" && c.agents[block.ToolID].ID != "" {
			status := "succeeded"
			if block.Error {
				status = "failed"
			}
			if frame.Result.Status == "async_launched" || frame.Result.Status == "running" {
				status = "running"
			}
			if err := c.update(protocol.SubagentUpdate{ID: block.ToolID, Status: status, NativeID: frame.Result.AgentID, Entry: &protocol.SubagentEntry{ID: frame.UUID + ":result:" + block.ToolID, Kind: "result", Text: subagent.Text(block.Content), Status: status}}); err != nil {
				return err
			}
		}
		if parent == "" {
			continue
		}
		entry := &protocol.SubagentEntry{ID: fmt.Sprintf("%s:%d", frame.UUID, index)}
		switch block.Type {
		case "text":
			entry.Kind, entry.Text = "message", block.Text
		case "tool_use":
			entry.ID, entry.Kind, entry.Tool, entry.Input, entry.Status = block.ID, "tool", block.Name, block.Input, "running"
			if block.Name == "AskUserQuestion" {
				if c.questions == nil {
					c.questions = map[string]bool{}
				}
				c.questions[block.ID] = true
				if err := c.update(protocol.SubagentUpdate{ID: parent, Status: "waiting_user"}); err != nil {
					return err
				}
			}
		case "tool_result":
			entry.ID, entry.Kind, entry.Output, entry.Status = block.ToolID, "tool", block.Content, "succeeded"
			if block.Error {
				entry.Status = "failed"
			}
			if c.questions[block.ToolID] {
				delete(c.questions, block.ToolID)
				if err := c.update(protocol.SubagentUpdate{ID: parent, Status: "running"}); err != nil {
					return err
				}
			}
		default:
			continue
		}
		if err := c.update(protocol.SubagentUpdate{ID: parent, Entry: entry}); err != nil {
			return err
		}
	}
	return nil
}

// A process exit without a native bookend leaves an uncertain task, including
// after an interrupted parent. Preserve that distinction for later viewing.
func (c *claudeSubagents) finish(interrupted bool) error {
	status := "unknown"
	if interrupted {
		status = "interrupted"
	}
	for id, agent := range c.agents {
		if agent.Status == "running" || agent.Status == "queued" || agent.Status == "waiting_user" {
			if err := c.update(protocol.SubagentUpdate{ID: id, Status: status}); err != nil {
				return err
			}
		}
	}
	return nil
}
