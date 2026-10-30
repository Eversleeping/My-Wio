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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// Gemini 0.66 exposes invoke_agent in ACP, but its policy lookup rejects the
// built-in agent. Keep this integration check until the upstream behavior changes.
func TestNativeGeminiGeneralistACPPolicy(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_V066_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_V066_CLI to run the Gemini 0.66.0 ACP policy check")
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
	offered, denied := false, false
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
		if n == 1 {
			offered = bytes.Contains(body, []byte(`"invoke_agent"`))
		}
		if bytes.Contains(body, []byte("denied by policy")) {
			denied = true
		}
		mu.Unlock()
		var part any = map[string]any{"text": "Delegation completed."}
		if n == 1 {
			part = map[string]any{"functionCall": map[string]any{"name": "invoke_agent", "args": map[string]any{"agent_name": "generalist", "prompt": "Return the test result"}}}
		}
		result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var eventsMu sync.Mutex
	var events []protocol.StreamEvent
	a := New(cli, func(e protocol.StreamEvent) error {
		eventsMu.Lock()
		events = append(events, e)
		eventsMu.Unlock()
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "agent-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "native-agent", Workspace: workspace, Prompt: "Delegate this task to the generalist", Model: "gemini-2.5-flash", ApprovalMode: "default"}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	eventsMu.Lock()
	defer eventsMu.Unlock()
	raw, _ := json.Marshal(events)
	if !offered || !denied || requests < 2 || !bytes.Contains(raw, []byte("denied by policy")) {
		t.Fatalf("ACP agent policy behavior changed: offered=%v denied=%v requests=%d events=%s", offered, denied, requests, raw)
	}
}
