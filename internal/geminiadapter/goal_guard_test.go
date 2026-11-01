package geminiadapter

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestGoalRepeatedRoundsIgnoreTextChunkBoundaries(t *testing.T) {
	g := &geminiGoalRun{}
	for round := 0; round < goalRepeatedRoundLimit; round++ {
		if round == 1 {
			g.activity("message", "same ")
			g.activity("message", "answer")
		} else {
			g.activity("message", "same answer")
		}
		if stopped := g.finishRound(); stopped != (round == goalRepeatedRoundLimit-1) {
			t.Fatalf("round %d stopped=%v", round, stopped)
		}
	}
	if err := g.record("completed", "model claim"); err != nil {
		t.Fatal(err)
	}
	if report, _ := g.result(); report != "blocked" {
		t.Fatal("completion bypassed repetition guard")
	}
}

func TestGoalEmptyRoundsAndChangingProgress(t *testing.T) {
	g := &geminiGoalRun{}
	for round := 0; round < 12; round++ {
		g.activity("message", fmt.Sprint(round))
		if g.finishRound() {
			t.Fatal("changing output was blocked")
		}
	}
	for round := 0; round < goalRepeatedRoundLimit; round++ {
		if stopped := g.finishRound(); stopped != (round == goalRepeatedRoundLimit-1) {
			t.Fatalf("empty round %d stopped=%v", round, stopped)
		}
	}
}

func TestGoalRepeatedToolsIncludeArgumentsAndResults(t *testing.T) {
	g := &geminiGoalRun{}
	for i := 0; i < 20; i++ {
		g.toolStarted("id", "read_file", json.RawMessage(fmt.Sprintf(`{"path":"%d.txt"}`, i)))
		if g.toolFinished("id", "read_file", "completed", json.RawMessage(`[]`)) {
			t.Fatal("different arguments blocked")
		}
	}
	for i := 0; i < 20; i++ {
		g.toolStarted("id", "read_file", json.RawMessage(`{"path":"a.txt"}`))
		if g.toolFinished("id", "read_file", "completed", json.RawMessage(fmt.Sprintf(`[%d]`, i))) {
			t.Fatal("different results blocked")
		}
	}
	for i := 0; i < goalRepeatedToolLimit; i++ {
		g.toolStarted("id", "read_file", json.RawMessage(`{"path":"a.txt"}`))
		if stopped := g.toolFinished("id", "read_file", "completed", json.RawMessage(`[]`)); stopped != (i == goalRepeatedToolLimit-1) {
			t.Fatalf("call %d stopped=%v", i, stopped)
		}
	}
}
