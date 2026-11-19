package geminiadapter

import (
	"encoding/json"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/subagent"
)

type geminiSubagents struct {
	stream, operation string
	emit              func(protocol.StreamEvent) error
	agents            map[string]protocol.SubagentUpdate
	entries           map[string]string
}

func (c *geminiSubagents) publish(update protocol.SubagentUpdate) error {
	update.OperationID = c.operation
	return c.emit(subagent.Event(c.stream, update))
}

func (c *geminiSubagents) update(raw json.RawMessage) error {
	var frame struct {
		Update struct {
			Type          string `json:"sessionUpdate"`
			ID            string `json:"toolCallId"`
			Title, Status string
			Content       json.RawMessage
			Meta          struct {
				Agent *struct {
					Name, State, Result string
					Prompt              json.RawMessage
					Activities          []struct{ ID, Type, Content, DisplayName, Description, Args, Status string } `json:"recentActivity"`
				} `json:"wioSubagent"`
			} `json:"_meta"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return nil
	}
	u := frame.Update
	if u.ID == "" {
		return nil
	}
	previous, exists := c.agents[u.ID]
	progress := u.Meta.Agent
	// Older ACP implementations include the native description even when they
	// cannot stream the executor callback.
	description := strings.HasPrefix(u.Title, "Running subagent '") || strings.HasPrefix(u.Title, "Delegating to agent '")
	if !exists && progress == nil && !description {
		return nil
	}
	if !exists {
		previous = protocol.SubagentUpdate{ID: u.ID, Name: u.Title, Status: "running", Detail: "result"}
	}
	next := previous
	if progress != nil {
		next.Name, next.Detail = progress.Name, "activity"
		if len(progress.Prompt) > 0 {
			next.Prompt = string(progress.Prompt)
		}
		if progress.State != "" {
			next.Status = subagent.Status(progress.State)
		}
	}
	if u.Status != "" {
		next.Status = subagent.Status(u.Status)
		// ACP completion describes the wrapping tool. The executor may have
		// already reported cancellation or failure inside that successful call.
		if u.Status == "completed" && (previous.Status == "failed" || previous.Status == "interrupted") {
			next.Status = previous.Status
		}
	}
	if !exists || previous != next {
		c.agents[u.ID] = next
		if err := c.publish(next); err != nil {
			return err
		}
	}
	if progress != nil {
		for _, item := range progress.Activities {
			if item.Type != "tool_call" || item.ID == "" {
				continue
			}
			entry := protocol.SubagentEntry{ID: item.ID, Kind: "tool", Tool: item.Content, Status: subagent.Status(item.Status), Text: item.Description}
			if json.Valid([]byte(item.Args)) {
				entry.Input = json.RawMessage(item.Args)
			}
			rawEntry, _ := json.Marshal(entry)
			key := u.ID + ":" + item.ID
			if c.entries[key] == string(rawEntry) {
				continue
			}
			c.entries[key] = string(rawEntry)
			if err := c.publish(protocol.SubagentUpdate{ID: u.ID, Entry: &entry}); err != nil {
				return err
			}
		}
	}
	if u.Status == "completed" || u.Status == "failed" {
		return c.publish(protocol.SubagentUpdate{ID: u.ID, Entry: &protocol.SubagentEntry{ID: u.ID + ":result", Kind: "result", Text: subagent.Text(u.Content), Status: next.Status}})
	}
	return nil
}

func (c *geminiSubagents) finish(interrupted bool) error {
	status := "unknown"
	if interrupted {
		status = "interrupted"
	}
	for id, agent := range c.agents {
		if agent.Status == "running" || agent.Status == "queued" || agent.Status == "waiting_user" {
			if err := c.publish(protocol.SubagentUpdate{ID: id, Status: status}); err != nil {
				return err
			}
		}
	}
	return nil
}
