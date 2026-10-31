package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestSessionUsageAggregatesFullGeminiHistory(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(kind, payload string, seconds int) {
		t.Helper()
		_, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: "gemini-usage", Kind: kind, Payload: json.RawMessage(payload), OccurredAt: base.Add(time.Duration(seconds) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("gemini.init", `{"operation_id":"one"}`, 0)
	add("gemini.result", `{"operation_id":"one","stats":{"input_tokens":1000,"output_tokens":100}}`, 30)
	add("gemini.result", `{"operation_id":"one","stats":{"input_tokens":1000,"output_tokens":100}}`, 31)
	add("gemini.result", `{"operation_id":"one","stats":{}}`, 31)
	add("gemini.turn.completed", `{"operation_id":"one"}`, 32)
	for i := 0; i < 510; i++ {
		add("gemini.message", `{}`, 40)
	}
	add("gemini.init", `{"operation_id":"two"}`, 120)
	add("gemini.result", `{"operation_id":"two","stats":{"input":2000,"output":200}}`, 150)
	add("gemini.turn.completed", `{"operation_id":"two"}`, 151)
	got, err := db.SessionUsage(ctx, "gemini-usage", "gemini", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens == nil || *got.InputTokens != 3000 || got.OutputTokens == nil || *got.OutputTokens != 300 || got.PerMinute == nil || *got.PerMinute != 3300 || got.OutputPerSecond == nil || *got.OutputPerSecond != 5 || got.Partial {
		t.Fatalf("incorrect full-session usage: %+v", got)
	}
	add("gemini.init", `{"operation_id":"three"}`, 180)
	add("gemini.result", `{"operation_id":"three","stats":{"input_tokens":"[REDACTED]","output_tokens":5}}`, 200)
	got, err = db.SessionUsage(ctx, "gemini-usage", "gemini", false)
	if err != nil || !got.Partial || got.PerMinute != nil || got.OutputTokens == nil || *got.OutputTokens != 305 || got.OutputPerSecond == nil || *got.OutputPerSecond != 3.8125 {
		t.Fatalf("missing input should not prevent an output rate: %+v %v", got, err)
	}
	add("gemini.init", `{"operation_id":"four"}`, 220)
	add("gemini.result", `{"operation_id":"four","stats":{"input_tokens":50}}`, 230)
	got, err = db.SessionUsage(ctx, "gemini-usage", "gemini", false)
	if err != nil || !got.Partial || got.OutputPerSecond != nil {
		t.Fatalf("missing output should make the rate unavailable: %+v %v", got, err)
	}
}

func TestSessionUsageUsesCodexCumulativeSnapshotsOnce(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(kind, payload string, seconds int) {
		t.Helper()
		_, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: "codex-usage", Kind: kind, Payload: json.RawMessage(payload), OccurredAt: base.Add(time.Duration(seconds) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("codex.turn.started", `{"turn":{"id":"one"}}`, 0)
	add("codex.thread.tokenUsage.updated", `{"usage":{"input_tokens":1000,"output_tokens":50}}`, 20)
	add("codex.turn.completed", `{"turn":{"id":"one"}}`, 30)
	add("codex.turn.started", `{"turn":{"id":"two"}}`, 100)
	add("codex.thread.tokenUsage.updated", `{"usage":{"input_tokens":1200,"output_tokens":90}}`, 120)
	add("codex.thread.tokenUsage.updated", `{"tokenUsage":"[REDACTED]"}`, 121)
	got, err := db.SessionUsage(ctx, "codex-usage", "codex", true)
	if err != nil || got.InputTokens == nil || *got.InputTokens != 1200 || got.OutputTokens == nil || *got.OutputTokens != 90 || got.PerMinute == nil || *got.PerMinute != 1548 || got.OutputPerSecond == nil || *got.OutputPerSecond != 1.8 || got.Partial {
		t.Fatalf("incorrect cumulative usage: %+v %v", got, err)
	}
	add("codex.turn.completed", `{"turn":{"id":"two"}}`, 130)
	got, err = db.SessionUsage(ctx, "codex-usage", "codex", false)
	if err != nil || got.PerMinute == nil || *got.PerMinute != 1290 || got.OutputPerSecond == nil || *got.OutputPerSecond != 1.5 {
		t.Fatalf("incorrect completed rate: %+v %v", got, err)
	}
	got, err = db.SessionUsage(ctx, "other-thread", "codex", false)
	if err != nil || got.InputTokens != nil || got.OutputTokens != nil || got.PerMinute != nil || got.OutputPerSecond != nil {
		t.Fatalf("unexpected data for empty thread: %+v %v", got, err)
	}
}

func TestGeminiGoalUsageSnapshotsAreCumulativeAndRemainPartialIfInterrupted(t *testing.T) {
	db := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(kind, payload string, seconds int) {
		t.Helper()
		if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: "goal-usage", Kind: kind, Payload: json.RawMessage(payload), OccurredAt: base.Add(time.Duration(seconds) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	add("gemini.init", `{"operation_id":"goal"}`, 0)
	add("gemini.goal.usage", `{"operation_id":"goal","stats":{"input_tokens":10,"output_tokens":5}}`, 10)
	add("gemini.goal.usage", `{"operation_id":"goal","stats":{"input_tokens":20,"output_tokens":10}}`, 20)
	got, err := db.SessionUsage(ctx, "goal-usage", "gemini", true)
	if err != nil || !got.Partial || got.InputTokens == nil || *got.InputTokens != 20 || got.OutputTokens == nil || *got.OutputTokens != 10 || got.OutputPerSecond != nil {
		t.Fatalf("incorrect in-flight goal usage: %+v %v", got, err)
	}
	add("gemini.result", `{"operation_id":"goal","stats":{"input_tokens":30,"output_tokens":15}}`, 30)
	add("gemini.turn.completed", `{"operation_id":"goal"}`, 31)
	got, err = db.SessionUsage(ctx, "goal-usage", "gemini", false)
	if err != nil || got.Partial || got.InputTokens == nil || *got.InputTokens != 30 || got.OutputTokens == nil || *got.OutputTokens != 15 || got.OutputPerSecond == nil || *got.OutputPerSecond != 0.5 {
		t.Fatalf("goal usage counted more than once: %+v %v", got, err)
	}
	for _, event := range []struct{ stream, kind, payload string }{
		{"interrupted", "gemini.init", `{"operation_id":"op"}`},
		{"interrupted", "gemini.goal.usage", `{"operation_id":"op","stats":{"input_tokens":10,"output_tokens":5}}`},
		{"interrupted", "gemini.turn.completed", `{"operation_id":"op","status":"interrupted"}`},
		{"missing", "gemini.init", `{"operation_id":"op"}`},
		{"missing", "gemini.goal.usage", `{"operation_id":"op","stats":{"input_tokens":10,"output_tokens":5}}`},
		{"missing", "gemini.result", `{"operation_id":"op","stats":{"output_tokens":10}}`},
	} {
		if _, err := db.AddEvent(ctx, protocol.StreamEvent{StreamID: event.stream, Kind: event.kind, Payload: json.RawMessage(event.payload), OccurredAt: base.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	interrupted, err := db.SessionUsage(ctx, "interrupted", "gemini", false)
	if err != nil || !interrupted.Partial || interrupted.InputTokens == nil || *interrupted.InputTokens != 10 || interrupted.OutputTokens == nil || *interrupted.OutputTokens != 5 || interrupted.OutputPerSecond != nil {
		t.Fatalf("interrupted goal lost its partial snapshot: %+v %v", interrupted, err)
	}
	missing, err := db.SessionUsage(ctx, "missing", "gemini", false)
	if err != nil || !missing.Partial || missing.InputTokens != nil || missing.OutputTokens == nil || *missing.OutputTokens != 10 {
		t.Fatalf("missing final usage was mistaken for an earlier complete count: %+v %v", missing, err)
	}
}
