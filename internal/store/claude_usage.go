package store

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

// Claude's result.usage is per native turn, including in streaming-input mode.
// modelUsage is deliberately not added: it includes usage restored on resume.
func (s *Store) claudeSessionUsage(ctx context.Context, threadID string, running bool) (SessionUsage, error) {
	usage := SessionUsage{}
	rows, err := s.DB.QueryxContext(ctx, s.Q(`SELECT kind,occurred_at,payload,event_id FROM events WHERE stream_id=? AND kind IN ('claude.init','claude.result','claude.turn.completed') ORDER BY sequence`), threadID)
	if err != nil {
		return usage, err
	}
	defer rows.Close()
	type counter struct{ input, output *int64 }
	type operation struct {
		start, end             time.Time
		completed, interrupted bool
		results                map[string]*counter
	}
	operations := map[string]*operation{}
	for rows.Next() {
		var kind, raw, eventID string
		var at time.Time
		if err := rows.Scan(&kind, &at, &raw, &eventID); err != nil {
			return usage, err
		}
		var p struct {
			OperationID string                     `json:"operation_id"`
			ResultID    string                     `json:"result_id"`
			Status      string                     `json:"status"`
			Usage       map[string]json.RawMessage `json:"usage"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil {
			usage.Partial = true
			continue
		}
		id := p.OperationID
		if id == "" {
			id = eventID
		}
		op := operations[id]
		if op == nil {
			op = &operation{results: map[string]*counter{}}
			operations[id] = op
		}
		switch kind {
		case "claude.init":
			if op.start.IsZero() {
				op.start = at
			}
		case "claude.result":
			id := p.ResultID
			if id == "" {
				// Older Agents already persist per-turn usage, without the native UUID.
				// The event store deduplicates their reliable transport by event_id.
				id = eventID
			}
			value := op.results[id]
			if value == nil {
				value = &counter{}
				op.results[id] = value
			}
			if input := claudeInputTokens(p.Usage); input != nil {
				value.input = input
			}
			if output := usageCount(p.Usage["output_tokens"]); output != nil {
				value.output = output
			}
			if !op.completed && at.After(op.end) {
				op.end = at
			}
		case "claude.turn.completed":
			op.completed = true
			op.interrupted = p.Status == "interrupted"
			if at.After(op.end) {
				op.end = at
			}
		}
	}
	if err := rows.Err(); err != nil {
		return usage, err
	}
	var seconds float64
	rateIncomplete := usage.Partial
	for _, op := range operations {
		if len(op.results) == 0 {
			if op.completed || !running {
				usage.Partial, rateIncomplete = true, true
			}
			continue
		}
		for _, value := range op.results {
			if value.input == nil || value.output == nil {
				usage.Partial = true
			}
			if value.output == nil {
				rateIncomplete = true
			}
			addUsageCount(&usage.InputTokens, value.input)
			addUsageCount(&usage.OutputTokens, value.output)
		}
		if op.interrupted || op.start.IsZero() || !op.end.After(op.start) {
			usage.Partial, rateIncomplete = true, true
			continue
		}
		seconds += op.end.Sub(op.start).Seconds()
	}
	if !usage.Partial && usage.InputTokens != nil && usage.OutputTokens != nil && seconds > 0 {
		value := (float64(*usage.InputTokens) + float64(*usage.OutputTokens)) * 60 / seconds
		usage.PerMinute = &value
	}
	if !rateIncomplete && usage.OutputTokens != nil && seconds > 0 {
		value := float64(*usage.OutputTokens) / seconds
		usage.OutputPerSecond = &value
	}
	return usage, nil
}

func addUsageCount(total **int64, value *int64) {
	if value == nil {
		return
	}
	if *total == nil {
		*total = new(int64)
	}
	**total += *value
}

func claudeInputTokens(usage map[string]json.RawMessage) *int64 {
	input := usageCount(usage["input_tokens"])
	if input == nil {
		return nil
	}
	for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		if raw, ok := usage[key]; ok {
			value := usageCount(raw)
			if value == nil || *value > math.MaxInt64-*input {
				return nil
			}
			*input += *value
		}
	}
	return input
}
