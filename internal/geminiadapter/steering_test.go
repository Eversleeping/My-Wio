package geminiadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestSteeringHookQueuesPersistsAndRejectsOtherTurns(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required for the official Gemini hook")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []protocol.StreamEvent
	var eventMu sync.Mutex
	a := New("", func(e protocol.StreamEvent) error {
		eventMu.Lock()
		defer eventMu.Unlock()
		events = append(events, e)
		return nil
	})
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	system := filepath.Join(t.TempDir(), "system.json")
	original := []byte(`{"hooks":{"BeforeModel":[{"hooks":[{"name":"existing","type":"command","command":"echo {}"}]}]},"model":{"name":"keep"}}`)
	if err := os.WriteFile(system, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	env, close, err := a.startSteering(ctx, "op", "thread", mergedEnvironment([]string{"GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + system, "GEMINI_CLI_HOME=" + home}))
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	settingsPath := ""
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "WIO_GEMINI_STEER_SCRIPT="); ok {
			settingsPath = v
		}
	}
	request := protocol.GeminiSteerCommand{ThreadID: "thread", OperationID: "op", RequestID: "hint-1", Prompt: "Focus on tests"}
	if a.Steer(request) == nil {
		t.Fatal("accepted before the hook became ready")
	}
	run := func() map[string]any {
		cmd := exec.Command("node", filepath.Join(filepath.Dir(settingsPath), "steering-hook.cjs"))
		cmd.Env = env
		cmd.Stdin = strings.NewReader(`{"llm_request":{"messages":[{"role":"user","content":"Original prompt"}]}}`)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook: %v %s", err, out)
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if len(run()) != 0 {
		t.Fatal("empty queue changed the request")
	}
	if err := a.Steer(request); err != nil {
		t.Fatal(err)
	}
	if err := a.Steer(request); err != nil {
		t.Fatal("identical retry must be idempotent")
	}
	other := request
	other.ThreadID = "other"
	if a.Steer(other) == nil {
		t.Fatal("cross-thread request accepted")
	}
	other = request
	other.OperationID = "later"
	if a.Steer(other) == nil {
		t.Fatal("cross-turn request accepted")
	}
	other = request
	other.Prompt = "Different"
	if a.Steer(other) == nil {
		t.Fatal("changed retry accepted")
	}
	for i := 0; i < 2; i++ {
		raw, _ := json.Marshal(run())
		if !bytes.Contains(raw, []byte("Focus on tests")) || !bytes.Contains(raw, []byte("Original prompt")) {
			t.Fatalf("hint lost: %s", raw)
		}
	}
	eventMu.Lock()
	if len(events) != 2 || events[0].Kind != "gemini.steer.ready" || events[1].Kind != "gemini.steer.applied" {
		t.Fatalf("unexpected delivery events: %+v", events)
	}
	eventMu.Unlock()
	current, _ := os.ReadFile(system)
	if !bytes.Equal(current, original) {
		t.Fatal("system settings changed")
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	var compact bytes.Buffer
	_ = json.Compact(&compact, raw)
	if !bytes.Contains(compact.Bytes(), []byte(`"name":"existing"`)) {
		t.Fatal("existing hooks lost")
	}
	cancel()
	if a.Steer(request) == nil {
		t.Fatal("accepted after cancellation")
	}
	close()
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatal("temporary settings leaked")
	}
}

// Run against the official installed CLI and a local fake model, without using
// external credentials or inference. This checks the real ACP/hook/tool path.
func TestGeminiNativeSteering(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_STEERING_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_STEERING_CLI to run the official CLI integration")
	}
	home := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"disableAutoUpdate":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	delivered := false
	toolPaired := false
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "countTokens") {
			_, _ = io.WriteString(w, `{"totalTokens":10}`)
			return
		}
		if !strings.Contains(r.URL.Path, "generateContent") && !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Log("model metadata request:", r.URL.Path)
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		mu.Lock()
		requests++
		n := requests
		if bytes.Contains(body, []byte("STEER_TEST_MARKER")) {
			delivered = true
		}
		if bytes.Contains(body, []byte("functionResponse")) {
			toolPaired = true
		}
		mu.Unlock()
		var part any = map[string]any{"text": "Steered successfully"}
		if n == 1 {
			part = map[string]any{"functionCall": map[string]any{"name": "run_shell_command", "args": map[string]any{"command": "node -e \"setTimeout(() => console.log('tool finished'), 200)\"", "description": "Wait briefly"}}}
		}
		result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var a *Adapter
	var ready, steered atomic.Bool
	a = New(cli, func(e protocol.StreamEvent) error {
		if e.Kind == "gemini.steer.ready" {
			ready.Store(true)
		}
		if e.Kind == "gemini.tool_use" && !steered.Load() {
			if !ready.Load() {
				return fmt.Errorf("hook was not invoked before tool execution")
			}
			if err := a.Steer(protocol.GeminiSteerCommand{ThreadID: "native", OperationID: "op", RequestID: "hint", Prompt: "STEER_TEST_MARKER: focus on tests"}); err != nil {
				return err
			}
			steered.Store(true)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "op", protocol.GeminiTurnCommand{Interactive: true, Steering: true, ThreadID: "native", Workspace: workspace, Prompt: "Use the shell tool once then reply", Model: "gemini-2.5-flash", ApprovalMode: "yolo"}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests < 2 || !steered.Load() || !delivered || !toolPaired {
		t.Fatalf("real CLI path: requests=%d steered=%v delivered=%v toolPaired=%v", requests, steered.Load(), delivered, toolPaired)
	}
}
