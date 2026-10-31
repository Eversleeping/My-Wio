package store

import (
	"context"
	"encoding/json"
	"time"
)

// SessionUsage reports only counters retained in the current transcript. Null
// means no trustworthy counter/rate was available, rather than zero usage.
type SessionUsage struct {
	InputTokens     *int64   `json:"input_tokens"`
	OutputTokens    *int64   `json:"output_tokens"`
	PerMinute       *float64 `json:"per_minute"` // Kept for clients using the previous metric.
	OutputPerSecond *float64 `json:"output_per_second"`
	Partial         bool     `json:"partial"`
}

type usageTurn struct {
	start, end                  time.Time
	input, output               *int64
	result, progress, completed bool
}

func usageCount(values ...json.RawMessage) *int64 {
	for _, raw := range values {
		var value int64
		if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &value) == nil && value >= 0 {
			return &value
		}
	}
	return nil
}

func (s *Store) SessionUsage(ctx context.Context, threadID, provider string, running bool) (SessionUsage, error) {
	usage := SessionUsage{}
	kinds := "'gemini.init','gemini.result','gemini.goal.usage','gemini.turn.completed'"
	if provider == "codex" {
		kinds = "'codex.turn.started','codex.turn.completed','codex.turn.failed','codex.turn.cancelled','codex.thread.tokenUsage.updated'"
	}
	rows, err := s.DB.QueryxContext(ctx, s.Q("SELECT kind,occurred_at,payload,event_id FROM events WHERE stream_id=? AND kind IN ("+kinds+") ORDER BY sequence"), threadID)
	if err != nil {
		return usage, err
	}
	defer rows.Close()
	turns := make(map[string]*usageTurn)
	lastTurn := ""
	var lastUsageTime time.Time
	outputRateIncomplete := false
	getTurn := func(id string) *usageTurn {
		if turns[id] == nil {
			turns[id] = &usageTurn{}
		}
		return turns[id]
	}
	for rows.Next() {
		var kind, payload, eventID string
		var at time.Time
		if err := rows.Scan(&kind, &at, &payload, &eventID); err != nil {
			return usage, err
		}
		var p struct {
			OperationID string `json:"operation_id"`
			TurnID      string `json:"turnId"`
			Turn        struct {
				ID string `json:"id"`
			} `json:"turn"`
			Stats map[string]json.RawMessage `json:"stats"`
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &p) != nil {
			continue
		}
		if provider == "gemini" {
			id := p.OperationID
			if id == "" {
				id = eventID
			}
			turn := getTurn(id)
			switch kind {
			case "gemini.init":
				if turn.start.IsZero() {
					turn.start = at
				}
			case "gemini.result", "gemini.goal.usage":
				if kind == "gemini.goal.usage" && turn.result {
					continue
				}
				if kind == "gemini.result" && turn.result {
					// Retried final events may omit counters. Preserve earlier valid
					// values while still accepting a later valid correction.
					if input := usageCount(p.Stats["input_tokens"], p.Stats["input"]); input != nil {
						turn.input = input
					}
					if output := usageCount(p.Stats["output_tokens"], p.Stats["output"]); output != nil {
						turn.output = output
					}
					continue
				}
				if !turn.result {
					turn.end = at
				}
				if kind == "gemini.result" {
					turn.result = true
				}
				turn.progress = true
				turn.input = usageCount(p.Stats["input_tokens"], p.Stats["input"])
				turn.output = usageCount(p.Stats["output_tokens"], p.Stats["output"])
			case "gemini.turn.completed":
				turn.completed = true
			}
			continue
		}
		if kind == "codex.thread.tokenUsage.updated" {
			input, output := usageCount(p.Usage["input_tokens"]), usageCount(p.Usage["output_tokens"])
			// Each update is the native session's cumulative total, not a delta.
			if input != nil && output != nil {
				usage.InputTokens, usage.OutputTokens, lastUsageTime = input, output, at
			}
			continue
		}
		id := p.TurnID
		if id == "" {
			id = p.Turn.ID
		}
		if kind == "codex.turn.started" {
			if id == "" {
				id = eventID
			}
			lastTurn = id
			turn := getTurn(id)
			if turn.start.IsZero() {
				turn.start = at
			}
		} else {
			if id == "" {
				id = lastTurn
			}
			if id == "" {
				usage.Partial = true
				outputRateIncomplete = true
				continue
			}
			turn := getTurn(id)
			if !turn.completed {
				turn.end, turn.completed = at, true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return usage, err
	}
	var seconds float64
	for _, turn := range turns {
		if provider == "gemini" {
			if !turn.result && !turn.progress {
				if turn.completed {
					usage.Partial = true
					outputRateIncomplete = true
				}
				continue
			}
			if !turn.result {
				usage.Partial = true
				outputRateIncomplete = true
			}
			if turn.input == nil || turn.output == nil {
				usage.Partial = true
			}
			if turn.output == nil {
				outputRateIncomplete = true
			}
			if turn.input != nil {
				if usage.InputTokens == nil {
					usage.InputTokens = new(int64)
				}
				*usage.InputTokens += *turn.input
			}
			if turn.output != nil {
				if usage.OutputTokens == nil {
					usage.OutputTokens = new(int64)
				}
				*usage.OutputTokens += *turn.output
			}
		} else if !turn.completed {
			if !running || lastUsageTime.IsZero() {
				continue
			}
			turn.end = lastUsageTime
		}
		if turn.start.IsZero() || turn.end.IsZero() || !turn.end.After(turn.start) {
			usage.Partial = true
			outputRateIncomplete = true
			continue
		}
		seconds += turn.end.Sub(turn.start).Seconds()
	}
	if !usage.Partial && usage.InputTokens != nil && usage.OutputTokens != nil && seconds > 0 {
		value := (float64(*usage.InputTokens) + float64(*usage.OutputTokens)) * 60 / seconds
		usage.PerMinute = &value
	}
	if !outputRateIncomplete && usage.OutputTokens != nil && seconds > 0 {
		value := float64(*usage.OutputTokens) / seconds
		usage.OutputPerSecond = &value
	}
	return usage, nil
}
