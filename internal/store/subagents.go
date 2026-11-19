package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/session"
	"github.com/wio-platform/wio/internal/subagent"
)

type Subagent struct {
	protocol.SubagentUpdate
	UpdatedAt time.Time `json:"updated_at"`
	Summary   string    `json:"summary,omitempty"`
}

func mergeSubagent(previous Subagent, update protocol.SubagentUpdate, at time.Time) Subagent {
	previous.ID = update.ID
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
	if update.OperationID != "" {
		previous.OperationID = update.OperationID
	}
	if update.Entry != nil {
		previous.Summary = update.Entry.Text
	}
	previous.UpdatedAt = at
	return previous
}

func (s *Store) subagentField(field string) string {
	if s.isPostgres() {
		return "CAST(payload AS jsonb)#>>'{" + strings.ReplaceAll(field, ".", ",") + "}'"
	}
	return "json_extract(payload,'$." + field + "')"
}

// ReadSubagents reads compact metadata independently of transcript pagination.
// Old Codex coordination events remain visible after upgrading the control plane.
func (s *Store) ReadSubagents(ctx context.Context, ref session.Ref) ([]Subagent, error) {
	query := "SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND ((kind=? AND " + s.subagentField("entry") + " IS NULL)"
	args := []any{ref.ID, protocol.EventSessionSubagentUpdated}
	legacy := map[string][]string{
		session.Codex:  {protocol.EventCodexItemStarted, protocol.EventCodexItemCompleted},
		session.Claude: {protocol.EventClaudeToolUse, protocol.EventClaudeToolResult},
		session.Gemini: {protocol.EventGeminiToolUse, protocol.EventGeminiToolResult},
	}[ref.Engine]
	if len(legacy) == 2 {
		query += " OR (kind IN (?,?)"
		args = append(args, legacy[0], legacy[1])
		if ref.Engine == session.Codex {
			query += " AND " + s.subagentField("item.type") + " IN (?,?,?)"
			args = append(args, "collabToolCall", "collabAgentToolCall", "subAgentActivity")
		}
		query += ")"
	}
	query += ") ORDER BY sequence"
	events, err := s.eventRows(ctx, s.Q(query), args...)
	if err != nil {
		return nil, err
	}
	agents := map[string]Subagent{}
	for _, event := range events {
		var updates []protocol.SubagentUpdate
		if event.Kind == protocol.EventSessionSubagentUpdated {
			var update protocol.SubagentUpdate
			if json.Unmarshal(event.Payload, &update) == nil {
				updates = append(updates, update)
			}
		} else if ref.Engine == session.Codex {
			updates = subagent.CodexUpdates(event.Payload)
		} else {
			updates = subagent.ToolUpdate(ref.Engine, event.Kind, event.Payload, func(id string) bool { return agents[id].ID != "" })
		}
		for _, update := range updates {
			if update.ID == "" {
				continue
			}
			previous := agents[update.ID]
			// Native collection supersedes old coordination summaries for the
			// same child. A completed send/wait must not reset its actual state.
			if event.Kind != protocol.EventSessionSubagentUpdated && (previous.Detail == "activity" || previous.OperationID != "") {
				continue
			}
			agents[update.ID] = mergeSubagent(previous, update, event.OccurredAt)
		}
	}
	activityQuery := "SELECT event_id," + s.subagentField("id") + " AS stream_id,sequence,kind,occurred_at,'{}' AS payload FROM events WHERE stream_id=? AND sequence IN (SELECT MAX(sequence) FROM events WHERE stream_id=? AND kind=? AND " + s.subagentField("id") + " IS NOT NULL GROUP BY " + s.subagentField("id") + ")"
	activity, err := s.eventRows(ctx, s.Q(activityQuery), ref.ID, ref.ID, protocol.EventSessionSubagentUpdated)
	if err != nil {
		return nil, err
	}
	for _, event := range activity {
		agent, exists := agents[event.StreamID]
		if exists && event.OccurredAt.After(agent.UpdatedAt) {
			agent.UpdatedAt = event.OccurredAt
			agents[event.StreamID] = agent
		}
	}
	items := make([]Subagent, 0, len(agents))
	for _, agent := range agents {
		items = append(items, agent)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

func (s *Store) SubagentEvents(ctx context.Context, parent, id string, after int64, limit int) ([]protocol.StreamEvent, error) {
	return s.eventRows(ctx, s.Q("SELECT event_id,stream_id,sequence,kind,occurred_at,payload FROM events WHERE stream_id=? AND kind=? AND "+s.subagentField("id")+"=? AND "+s.subagentField("entry")+" IS NOT NULL AND sequence>? ORDER BY sequence LIMIT ?"), parent, protocol.EventSessionSubagentUpdated, id, after, limit)
}
