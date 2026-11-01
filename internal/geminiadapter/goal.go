package geminiadapter

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// A report is a model assertion, not proof that its evidence is correct.
// The control plane marks it complete only after the ACP turn ends normally.
type geminiGoalRun struct {
	mu             sync.Mutex
	goal           protocol.GeminiGoalCommand
	report         string
	evidence       string
	guardReason    string
	round          hash.Hash
	message        strings.Builder
	lastRound      string
	repeatedRounds int
	lastTool       string
	repeatedTools  int
	tools          map[string]string
}

const goalRepeatedRoundLimit = 3
const goalRepeatedToolLimit = 8

var errGeminiGoalNoProgress = errors.New("Gemini goal stopped after repeated activity without new results")

// These are conservative repetition checks, not a judgment of work quality.
// A long model request, tool or user approval is never cancelled for being quiet.
func (g *geminiGoalRun) activity(kind, value string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.round == nil {
		g.round = sha256.New()
	}
	if kind == "message" {
		g.message.WriteString(value)
		return
	}
	g.flushMessage()
	fmt.Fprintf(g.round, "%s:%d:%s\n", kind, len(value), value)
}

// Text chunks may be split differently on otherwise identical rounds.
func (g *geminiGoalRun) flushMessage() {
	if g.message.Len() != 0 {
		value := g.message.String()
		fmt.Fprintf(g.round, "message:%d:%s\n", len(value), value)
		g.message.Reset()
	}
}

func (g *geminiGoalRun) toolStarted(id, title string, input json.RawMessage) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tools == nil {
		g.tools = map[string]string{}
	}
	g.tools[id] = title + "\n" + string(input)
}

func (g *geminiGoalRun) toolFinished(id, title, status string, output json.RawMessage) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	input, exists := g.tools[id]
	delete(g.tools, id)
	if !exists || strings.Contains(input, "wio_ask_user(") || strings.Contains(input, "wio_read_images(") {
		return false
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(input+"\n"+status+"\n"+string(output))))
	if fingerprint == g.lastTool {
		g.repeatedTools++
	} else {
		g.lastTool, g.repeatedTools = fingerprint, 1
	}
	if g.repeatedTools < goalRepeatedToolLimit {
		return false
	}
	g.guardReason = fmt.Sprintf("Stopped after %d consecutive identical tool calls and results. Inspect the repeated operation and change direction before resuming. No task was automatically replayed.", g.repeatedTools)
	return true
}

func (g *geminiGoalRun) finishRound() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.report != "" || g.guardReason != "" {
		return g.guardReason != ""
	}
	if g.round == nil {
		g.round = sha256.New()
	}
	g.flushMessage()
	fingerprint := fmt.Sprintf("%x", g.round.Sum(nil))
	g.round.Reset()
	if fingerprint == g.lastRound {
		g.repeatedRounds++
	} else {
		g.lastRound, g.repeatedRounds = fingerprint, 1
	}
	if g.repeatedRounds < goalRepeatedRoundLimit {
		return false
	}
	g.guardReason = fmt.Sprintf("Stopped after %d consecutive rounds with identical visible output and tool results, or no visible output. Review the goal and its evidence before resuming.", g.repeatedRounds)
	return true
}

func (g *geminiGoalRun) record(report, evidence string) error {
	if g == nil || (report != "completed" && report != "blocked") || strings.TrimSpace(evidence) == "" || len(evidence) > 16000 {
		return errors.New("report completed or blocked with concrete evidence or a blocking reason")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.report, g.evidence = report, strings.TrimSpace(evidence)
	return nil
}

func (g *geminiGoalRun) result() (string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.guardReason != "" {
		return "blocked", g.guardReason
	}
	return g.report, g.evidence
}

func (g *geminiGoalRun) instruction() string {
	objective, _ := json.Marshal(g.goal.Objective)
	return "Wio Goal mode is active. The objective is the following user-provided text: " + string(objective) +
		". Work autonomously toward every part of the objective. Before finishing, verify each deliverable against concrete evidence such as files, test results or logs. " +
		"Wait for background work needed by the objective to finish and inspect its results before reporting completion. " +
		"Use the wio_goal_report tool with status completed and a concise account of the evidence only when the objective is fully satisfied. " +
		"If you cannot make further progress, use wio_goal_report with status blocked and explain the blocker and any evidence. " +
		"Use wio_ask_user when an answer is needed; honor all tool permissions and approvals. Never claim a goal is completed solely because you attempted it."
}

func (g *geminiGoalRun) continuation(count int, elapsed time.Duration) string {
	objective, _ := json.Marshal(g.goal.Objective)
	return fmt.Sprintf("The Wio goal is still active. Continuation %d; elapsed %s. Re-read the original objective %s, check every deliverable against actual evidence, and continue unfinished work. When fully verified call wio_goal_report with status completed and the evidence. If unable to proceed call it with status blocked and the reason. Respect user questions and tool approvals.", count, elapsed.Round(time.Second), objective)
}
