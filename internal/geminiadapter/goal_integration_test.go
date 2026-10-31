package geminiadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Exercise the official Gemini CLI's ACP + MCP path against a local model;
// this does not use any real credentials or remote inference.
func TestNativeGeminiGoalContinuesReportsAndCountsUsage(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_V066_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_V066_CLI to run the official Gemini CLI integration")
	}
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"disableAutoUpdate":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	toolName := ""
	var findTool func(any)
	findTool = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if name, ok := value["name"].(string); ok && strings.HasSuffix(name, "wio_goal_report") {
				toolName = name
			}
			for _, child := range value {
				findTool(child)
			}
		case []any:
			for _, child := range value {
				findTool(child)
			}
		}
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "countTokens") {
			_, _ = io.WriteString(w, `{"totalTokens":10}`)
			return
		}
		if !strings.Contains(r.URL.Path, "generateContent") && !strings.Contains(r.URL.Path, "streamGenerateContent") {
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		mu.Lock()
		requests++
		n := requests
		var input any
		_ = json.Unmarshal(body, &input)
		findTool(input)
		name := toolName
		mu.Unlock()
		var part any = map[string]any{"text": "A partial answer."}
		if n == 2 && name != "" {
			part = map[string]any{"functionCall": map[string]any{"name": name, "args": map[string]any{"status": "completed", "evidence": "Verified output.txt and the test result."}}}
		} else if n > 2 {
			part = map[string]any{"text": "The goal is complete."}
		}
		response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var eventMu sync.Mutex
	var events []protocol.StreamEvent
	a := New(cli, func(e protocol.StreamEvent) error {
		eventMu.Lock()
		events = append(events, e)
		eventMu.Unlock()
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "goal-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "goal-thread", Workspace: workspace, Prompt: "Complete the sample task", Model: "gemini-2.5-flash", ApprovalMode: "default", Goal: &protocol.GeminiGoalCommand{ID: "goal-id", Objective: "Complete the sample task", MaxContinuations: 3, MaxSeconds: 60}}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
	mu.Lock()
	defer mu.Unlock()
	eventMu.Lock()
	defer eventMu.Unlock()
	raw, _ := json.Marshal(events)
	if err != nil || toolName == "" || requests != 3 || !strings.Contains(string(raw), `"report":"completed"`) || !strings.Contains(string(raw), `"input_tokens":30`) || !strings.Contains(string(raw), `"output_tokens":15`) {
		t.Fatalf("goal ACP: err=%v tool=%q requests=%d events=%s", err, toolName, requests, raw)
	}
}

func TestGoalStopsAtLimitWithoutACompletionReport(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "prompt.json"))
	t.Setenv("WIO_GEMINI_TEST_USAGE_RESULT", `{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":5}}`)
	executable, _ := os.Executable()
	var events []protocol.StreamEvent
	a := New(executable, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := a.Run(ctx, "goal-limit", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Prompt: "Do the work", Model: "flash", ApprovalMode: "default", Goal: &protocol.GeminiGoalCommand{ID: "goal-id", Objective: "Do the work", MaxContinuations: 2, MaxSeconds: 60}})
	raw, _ := json.Marshal(events)
	if err != nil || !strings.Contains(string(raw), `"report":"blocked"`) || !strings.Contains(string(raw), `"continuations":2`) || !strings.Contains(string(raw), `"input_tokens":30`) || !strings.Contains(string(raw), `"output_tokens":15`) {
		t.Fatalf("goal limit: err=%v events=%s", err, raw)
	}
}

func TestGoalDoesNotContinueAfterNonNormalStop(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "prompt.json"))
	executable, _ := os.Executable()
	for _, reason := range []string{"max_tokens", "max_turn_requests", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			t.Setenv("WIO_GEMINI_TEST_USAGE_RESULT", fmt.Sprintf(`{"stopReason":%q,"usage":{"inputTokens":10,"outputTokens":5}}`, reason))
			var events []protocol.StreamEvent
			a := New(executable, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := a.Run(ctx, "goal-stop", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Prompt: "Do the work", Model: "flash", ApprovalMode: "default", Goal: &protocol.GeminiGoalCommand{ID: "goal-id", Objective: "Do the work", MaxContinuations: 2, MaxSeconds: 60}})
			var snapshots, progress int
			for _, event := range events {
				if event.Kind == "gemini.goal.usage" {
					snapshots++
				}
				if event.Kind == "gemini.goal.progress" {
					progress++
				}
			}
			if err == nil || snapshots != 1 || progress != 0 {
				t.Fatalf("reason=%s err=%v snapshots=%d progress=%d events=%+v", reason, err, snapshots, progress, events)
			}
		})
	}
}

func TestGoalRetainsCompletedRoundUsageWhenInterrupted(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "prompt.json"))
	t.Setenv("WIO_GEMINI_TEST_USAGE_RESULT", `{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":5}}`)
	executable, _ := os.Executable()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []protocol.StreamEvent
	a := New(executable, func(event protocol.StreamEvent) error {
		events = append(events, event)
		if event.Kind == "gemini.goal.usage" {
			cancel()
		}
		return nil
	})
	err := a.Run(ctx, "goal-interrupt", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Prompt: "Do the work", Model: "flash", ApprovalMode: "default", Goal: &protocol.GeminiGoalCommand{ID: "goal-id", Objective: "Do the work", MaxContinuations: 2, MaxSeconds: 60}})
	raw, _ := json.Marshal(events)
	if err == nil || !strings.Contains(string(raw), `"input_tokens":10`) || !strings.Contains(string(raw), `"output_tokens":5`) || strings.Contains(string(raw), `"kind":"gemini.result"`) {
		t.Fatalf("interrupted usage: err=%v events=%s", err, raw)
	}
}
