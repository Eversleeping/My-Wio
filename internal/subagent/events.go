// Package subagent translates native delegation events without changing the
// lifecycle or transcript of the parent session.
package subagent

import (
	"encoding/json"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

func Status(value string) string {
	switch strings.ToLower(value) {
	case "pendinginit", "pending", "queued":
		return "queued"
	case "running", "in_progress", "inprogress", "started", "active":
		return "running"
	case "completed", "succeeded", "success":
		return "succeeded"
	case "errored", "error", "failed":
		return "failed"
	case "interrupted", "stopped", "killed", "cancelled", "shutdown":
		return "interrupted"
	case "waiting_user", "paused":
		return "waiting_user"
	default:
		return "unknown"
	}
}

func Event(stream string, update protocol.SubagentUpdate) protocol.StreamEvent {
	raw, _ := json.Marshal(update)
	return protocol.StreamEvent{StreamID: stream, Kind: protocol.EventSessionSubagentUpdated, Payload: raw}
}

func Text(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
		if block.Type == "content" {
			var content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(block.Content, &content) == nil && content.Type == "text" {
				parts = append(parts, content.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
