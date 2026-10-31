package geminiadapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// A report is a model assertion, not proof that its evidence is correct.
// The control plane marks it complete only after the ACP turn ends normally.
type geminiGoalRun struct {
	mu       sync.Mutex
	goal     protocol.GeminiGoalCommand
	report   string
	evidence string
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
