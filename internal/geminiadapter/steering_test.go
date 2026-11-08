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
	// Gemini's shell tools inherit this environment: the queue credential must
	// only be in the private per-turn file the hook reads.
	configPath := filepath.Join(filepath.Dir(settingsPath), steeringConfigFile)
	info, err := os.Stat(configPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("steering credential file: %v %v", info, err)
	}
	var steeringConfig struct{ URL, Token string }
	if raw, err := os.ReadFile(configPath); err != nil || json.Unmarshal(raw, &steeringConfig) != nil || steeringConfig.Token == "" || steeringConfig.URL == "" {
		t.Fatalf("invalid steering credential file: %v", err)
	}
	for _, entry := range env {
		if strings.Contains(entry, steeringConfig.Token) || strings.HasPrefix(entry, "WIO_GEMINI_STEER_AUTH=") || strings.HasPrefix(entry, "WIO_GEMINI_STEER_URL=") {
			t.Fatalf("steering credential leaked into the CLI environment: %s", strings.SplitN(entry, "=", 2)[0])
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
	for _, path := range []string{settingsPath, configPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("temporary steering files leaked")
		}
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
	shellSucceeded := false
	imageDelivered := false
	imageTool := ""
	var findImageTool func(any)
	findImageTool = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if name, ok := value["name"].(string); ok && strings.HasSuffix(name, "wio_read_images") {
				imageTool = name
			}
			for _, child := range value {
				findImageTool(child)
			}
		case []any:
			for _, child := range value {
				findImageTool(child)
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
			t.Log("model metadata request:", r.URL.Path)
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		mu.Lock()
		requests++
		n := requests
		var input any
		_ = json.Unmarshal(body, &input)
		findImageTool(input)
		name := imageTool
		if n >= 3 && bytes.Contains(body, []byte(`"inlineData"`)) && bytes.Contains(body, []byte("iVBORw0KGgo")) {
			imageDelivered = true
		}
		if bytes.Contains(body, []byte("STEER_TEST_MARKER")) {
			delivered = true
		}
		if bytes.Contains(body, []byte("functionResponse")) {
			toolPaired = true
			shellSucceeded = bytes.Contains(body, []byte("tool finished"))
		}
		mu.Unlock()
		var part any = map[string]any{"text": "Steered successfully"}
		if n == 1 {
			part = map[string]any{"functionCall": map[string]any{"name": "run_shell_command", "args": map[string]any{"command": "node -e \"console.log('tool finished')\" | node -e \"process.stdin.pipe(process.stdout)\"", "description": "Run a shell pipeline"}}}
		}
		if n == 2 && name != "" {
			part = map[string]any{"functionCall": map[string]any{"name": name, "args": map[string]any{"request_id": "hint"}}}
		}
		result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var a *Adapter
	var ready, steered atomic.Bool
	var questions atomic.Int32
	var applied atomic.Int32
	sessionID := ""
	a = New(cli, func(e protocol.StreamEvent) error {
		if e.Kind == "gemini.init" {
			var p struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			sessionID = p.SessionID
		}
		if e.Kind == "gemini.steer.applied" {
			applied.Add(1)
		}
		if e.Kind == "gemini.question" {
			questions.Add(1)
			return fmt.Errorf("YOLO shell pipeline unexpectedly requested approval")
		}
		if e.Kind == "gemini.steer.ready" {
			ready.Store(true)
		}
		if e.Kind == "gemini.tool_use" && !steered.Load() {
			if !ready.Load() {
				return fmt.Errorf("hook was not invoked before tool execution")
			}
			if err := a.Steer(protocol.GeminiSteerCommand{ThreadID: "native", OperationID: "op", RequestID: "hint", Prompt: "STEER_TEST_MARKER: focus on tests", Images: []protocol.TurnImage{{DataURL: testSteeringPNG}}}); err != nil {
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
	// MCP image results must survive a fresh CLI process and native resume.
	mu.Lock()
	imageDelivered = false
	mu.Unlock()
	if sessionID == "" {
		t.Fatal("missing native session")
	}
	err = a.RunInTrustedWorkspaceWithEnvironment(ctx, "op-resume", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "native", SessionID: sessionID, Workspace: workspace, Prompt: "Continue with the image from the last task", Model: "gemini-2.5-flash", ApprovalMode: "yolo"}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests < 3 || !steered.Load() || !delivered || !imageDelivered || !toolPaired || !shellSucceeded || questions.Load() != 0 || applied.Load() != 1 {
		t.Fatalf("real CLI path: requests=%d steered=%v delivered=%v image=%v toolPaired=%v shellSucceeded=%v questions=%d applied=%d tool=%s", requests, steered.Load(), delivered, imageDelivered, toolPaired, shellSucceeded, questions.Load(), applied.Load(), imageTool)
	}
}

const testSteeringPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="

func TestSteeringImagesAreScopedIdempotentAndAppliedOnlyWhenRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applied := 0
	a := New("", func(e protocol.StreamEvent) error {
		if e.Kind == "gemini.steer.applied" {
			applied++
		}
		return nil
	})
	a.steering = map[string]*steeringTurn{"op": {ctx: ctx, thread: "thread", ready: true}}
	request := protocol.GeminiSteerCommand{ThreadID: "thread", OperationID: "op", RequestID: "image", Images: []protocol.TurnImage{{DataURL: testSteeringPNG}}}
	if err := a.Steer(request); err != nil {
		t.Fatal(err)
	}
	if err := a.Steer(request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Images = append(changed.Images, changed.Images...)
	if a.Steer(changed) == nil {
		t.Fatal("changed image retry accepted")
	}
	if applied != 0 {
		t.Fatal("unread image marked delivered")
	}
	if _, err := a.readSteeringImages("op", "other", "image"); err == nil {
		t.Fatal("cross-thread image exposed")
	}
	if _, err := a.readSteeringImages("other", "thread", "image"); err == nil {
		t.Fatal("cross-operation image exposed")
	}
	for i := 0; i < 2; i++ {
		content, err := a.readSteeringImages("op", "thread", "image")
		if err != nil || len(content) != 2 || content[1].(map[string]string)["type"] != "image" {
			t.Fatalf("image result: %+v %v", content, err)
		}
	}
	if applied != 1 {
		t.Fatalf("applied %d times", applied)
	}
	for i := 1; i < 4; i++ {
		request.RequestID = fmt.Sprint(i)
		if err := a.Steer(request); err != nil {
			t.Fatal(err)
		}
	}
	request.RequestID = "over-limit"
	if a.Steer(request) == nil {
		t.Fatal("accepted fifth image")
	}
	cancel()
	if _, err := a.readSteeringImages("op", "thread", "image"); err == nil {
		t.Fatal("cancelled image exposed")
	}
}
