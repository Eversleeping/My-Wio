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

func TestNativeGeminiPlanMode(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_STEERING_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_STEERING_CLI to run the official CLI integration")
	}
	for _, test := range []struct {
		name, tool string
		approve    bool
		change     bool
	}{
		{name: "blocks project writes", tool: "write_file"},
		{name: "approved plan", tool: "exit_plan_mode", approve: true},
		{name: "changed plan", tool: "exit_plan_mode", approve: true, change: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
				t.Fatal(err)
			}
			settings := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"disableAutoUpdate":true,"plan":{"directory":".gemini/plans"}}}`
			if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			plans := filepath.Join(workspace, ".gemini", "plans")
			if err := os.MkdirAll(plans, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(plans, "example.md"), []byte("# Plan\n\nPLAN_CONTENT_MARKER: inspect then implement.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			requests := 0
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
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
				mu.Unlock()
				var part any = map[string]any{"text": "Plan test complete."}
				if n == 1 {
					args := map[string]any{"plan_filename": "example.md"}
					if test.tool == "write_file" {
						args = map[string]any{"file_path": filepath.Join(workspace, "blocked.txt"), "content": "MUST NOT WRITE"}
					}
					part = map[string]any{"functionCall": map[string]any{"name": test.tool, "args": args}}
				}
				result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5}, "modelVersion": "gemini-2.5-flash"}
				raw, _ := json.Marshal(result)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
			}))
			defer model.Close()
			var a *Adapter
			var eventMu sync.Mutex
			var events []protocol.StreamEvent
			a = New(cli, func(e protocol.StreamEvent) error {
				eventMu.Lock()
				events = append(events, e)
				eventMu.Unlock()
				if e.Kind == "gemini.question" && test.approve {
					var payload struct {
						Questions []protocol.UserInputQuestion `json:"questions"`
					}
					_ = json.Unmarshal(e.Payload, &payload)
					if len(payload.Questions) == 0 || len(payload.Questions[0].Options) == 0 {
						return fmt.Errorf("missing plan approval options: %s", e.Payload)
					}
					if test.change {
						if err := os.WriteFile(filepath.Join(plans, "example.md"), []byte("Changed after it was displayed"), 0600); err != nil {
							return err
						}
					}
					go func() {
						_ = a.Answer(protocol.GeminiAnswerCommand{ThreadID: "native-plan", OperationID: "plan-op", RequestID: e.EventID, Answers: map[string]protocol.UserInputAnswer{"permission": {Answers: []string{"Allow"}}}})
					}()
				}
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "plan-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "native-plan", Workspace: workspace, Prompt: "Plan the task", Model: "gemini-2.5-flash", ApprovalMode: "plan"}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(workspace, "blocked.txt")); !os.IsNotExist(err) {
				t.Fatal("plan mode wrote to a project file")
			}
			eventMu.Lock()
			defer eventMu.Unlock()
			raw, _ := json.Marshal(events)
			if !strings.Contains(string(raw), `"kind":"gemini.commands"`) || !strings.Contains(string(raw), `"name":"help"`) {
				t.Fatalf("native ACP commands were not discovered: %s", raw)
			}
			if test.approve && !test.change && (!strings.Contains(string(raw), "gemini.question") || !strings.Contains(string(raw), "gemini.mode")) {
				t.Fatalf("approval or mode transition not visible: %s", raw)
			}
			if test.approve && !strings.Contains(string(raw), "PLAN_CONTENT_MARKER") {
				t.Fatalf("approval omitted plan contents: %s", raw)
			}
			if test.change && (strings.Contains(string(raw), `"kind":"gemini.mode"`) || !strings.Contains(string(raw), "plan changed before approval")) {
				t.Fatalf("changed plan was approved: %s", raw)
			}
		})
	}
}

func TestNativeGeminiHelpInPlanMode(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_STEERING_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_STEERING_CLI to run the official CLI integration")
	}
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"disableAutoUpdate":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var events []protocol.StreamEvent
	a := New(cli, func(e protocol.StreamEvent) error { events = append(events, e); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "help-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "native-help", Workspace: workspace, Prompt: "/help", Model: "gemini-2.5-flash", ApprovalMode: "plan"}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:1", "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	if !strings.Contains(string(raw), "Gemini CLI Help:") {
		t.Fatalf("native /help did not run: %s", raw)
	}
	err = a.Run(ctx, "restore-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "native-help", Workspace: workspace, Prompt: "/restore", Model: "gemini-2.5-flash", ApprovalMode: "plan"})
	if err == nil || !strings.Contains(err.Error(), "unavailable in plan mode") {
		t.Fatalf("plan mode allowed /restore: %v", err)
	}
}
