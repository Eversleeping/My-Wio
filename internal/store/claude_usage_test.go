package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestClaudeUsageCountsNativeTurnsCachesAndFullHistory(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	add := func(kind, payload string, seconds int) {
		t.Helper()
		if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: "claude-usage", Kind: kind, Payload: json.RawMessage(payload), OccurredAt: base.Add(time.Duration(seconds) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	add("claude.init", `{"operation_id":"one"}`, 0)
	add("claude.result", `{"operation_id":"one","result_id":"r1","usage":{"input_tokens":5,"cache_creation_input_tokens":30,"cache_read_input_tokens":100,"output_tokens":10}}`, 10)
	// Steering produces another native turn within the same Wio operation.
	add("claude.result", `{"operation_id":"one","result_id":"r2","usage":{"input_tokens":10,"cache_read_input_tokens":15,"output_tokens":30}}`, 30)
	add("claude.result", `{"operation_id":"one","result_id":"r2","usage":{"input_tokens":10,"cache_read_input_tokens":15,"output_tokens":30}}`, 31)
	add("claude.result", `{"operation_id":"one","result_id":"r2","usage":{}}`, 32)
	add("claude.turn.completed", `{"operation_id":"one","status":"idle"}`, 40)
	for i := 0; i < 510; i++ {
		add("claude.message", `{}`, 45)
	}
	add("claude.init", `{"operation_id":"two"}`, 100)
	// Old Agents omit result_id; native modelUsage may include restored history.
	add("claude.result", `{"operation_id":"two","usage":{"input_tokens":40,"cache_read_input_tokens":60,"output_tokens":80},"modelUsage":{"model":{"inputTokens":5000}}}`, 120)
	add("claude.turn.completed", `{"operation_id":"two","status":"idle"}`, 120)
	got, err := db.SessionUsage(ctx, "claude-usage", "claude", false)
	if err != nil || got.InputTokens == nil || *got.InputTokens != 260 || got.OutputTokens == nil || *got.OutputTokens != 120 || got.OutputPerSecond == nil || *got.OutputPerSecond != 2 || got.PerMinute == nil || *got.PerMinute != 380 || got.Partial {
		t.Fatalf("incorrect session totals: %+v %v", got, err)
	}
	// A fresh operation with no result yet must not erase finished history.
	add("claude.init", `{"operation_id":"three"}`, 200)
	got, err = db.SessionUsage(ctx, "claude-usage", "claude", true)
	if err != nil || got.Partial || got.OutputTokens == nil || *got.OutputTokens != 120 {
		t.Fatalf("in-flight operation erased known totals: %+v %v", got, err)
	}
	add("claude.turn.completed", `{"operation_id":"three","status":"interrupted"}`, 210)
	got, err = db.SessionUsage(ctx, "claude-usage", "claude", false)
	if err != nil || !got.Partial || got.OutputPerSecond != nil || got.OutputTokens == nil || *got.OutputTokens != 120 {
		t.Fatalf("interrupted usage falsely complete: %+v %v", got, err)
	}
}

func TestClaudeUsageMissingCountersAndTiming(t *testing.T) {
	for _, tc := range []struct {
		name, usage   string
		start         bool
		input, output bool
		rate          bool
	}{
		{"redacted-input", `{"input_tokens":"[REDACTED]","output_tokens":10}`, true, false, true, true},
		{"missing-output", `{"input_tokens":5}`, true, true, false, false},
		{"missing-timing", `{"input_tokens":5,"output_tokens":10}`, false, true, true, false},
		{"invalid-cache", `{"input_tokens":5,"cache_read_input_tokens":-1,"output_tokens":10}`, true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testStore(t)
			ctx := context.Background()
			base := time.Now().UTC()
			if tc.start {
				if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: "usage", Kind: "claude.init", Payload: json.RawMessage(`{"operation_id":"op"}`), OccurredAt: base}); err != nil {
					t.Fatal(err)
				}
			}
			raw := json.RawMessage(`{"operation_id":"op","usage":` + tc.usage + `}`)
			if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: "usage", Kind: "claude.result", Payload: raw, OccurredAt: base.Add(10 * time.Second)}); err != nil {
				t.Fatal(err)
			}
			got, err := db.SessionUsage(ctx, "usage", "claude", false)
			if err != nil || !got.Partial || (got.InputTokens != nil) != tc.input || (got.OutputTokens != nil) != tc.output || (got.OutputPerSecond != nil) != tc.rate {
				t.Fatalf("incorrect incomplete usage: %+v %v", got, err)
			}
		})
	}
}
