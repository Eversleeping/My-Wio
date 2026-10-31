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

func TestLoopAliasesPreserveSettingsAndIsolateModels(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".gemini", "settings.json")
	original := []byte(`{// keep this comment
 "security":{"disableYoloMode":true},"modelConfigs":{"customAliases":{"loop-detection-double-check":{"extends":"base","modelConfig":{"model":"admin-model"}}}}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"GOOGLE_GEMINI_BASE_URL=http://localhost", "GEMINI_CLI_HOME=" + home, "WIO_GEMINI_LOOP_MODEL=stale"}
	var first []byte
	for _, model := range []string{"gateway-one", "gateway-two"} {
		env, err := loopModelEnvironment(base, model)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(env, "\n"), "WIO_GEMINI_LOOP_MODEL="+model) || strings.Contains(strings.Join(env, "\n"), "=stale") {
			t.Fatal("per-turn model not isolated")
		}
		settings, raw, _, err := readThinkingSettings(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte("keep this comment")) || settings["security"].(map[string]any)["disableYoloMode"] != true {
			t.Fatal("account settings lost")
		}
		aliases := settings["modelConfigs"].(map[string]any)["customAliases"].(map[string]any)
		if aliases["loop-detection"].(map[string]any)["modelConfig"].(map[string]any)["model"] != "${WIO_GEMINI_LOOP_MODEL:-gemini-3-flash-preview}" {
			t.Fatal("native fallback missing")
		}
		if aliases["loop-detection-double-check"].(map[string]any)["modelConfig"].(map[string]any)["model"] != "admin-model" {
			t.Fatal("explicit user model overwritten")
		}
		if first != nil && !bytes.Equal(first, raw) {
			t.Fatal("idempotent install rewrote settings")
		}
		first = raw
	}
	for _, model := range []string{"", "auto"} {
		env, err := loopModelEnvironment(base, model)
		if err != nil || strings.Contains(strings.Join(env, "\n"), "WIO_GEMINI_LOOP_MODEL=") {
			t.Fatal("automatic model inherited stale override")
		}
	}
	if err := os.WriteFile(path, []byte(`{"modelConfigs":{"customAliases":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loopModelEnvironment(base, "gateway"); err == nil {
		t.Fatal("invalid settings discarded")
	}
}

// Cross the real CLI's thirty-turn loop detection threshold. The fake gateway
// rejects every model except the selected one, reproducing the production setup.
func TestNativeGeminiLoopDetectionUsesSelectedGatewayModel(t *testing.T) {
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
	for i := 1; i <= 32; i++ {
		if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("sample-%d.txt", i)), []byte("sample"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	turns, checks := 0, 0
	var wrong []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "countTokens") {
			io.WriteString(w, `{"totalTokens":10}`)
			return
		}
		if !strings.Contains(r.URL.Path, "GenerateContent") && !strings.Contains(r.URL.Path, "generateContent") {
			io.WriteString(w, `{"models":[]}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(r.URL.Path, "/models/gemini-3.8-flash-high:") {
			if len(wrong) < 3 {
				wrong = append(wrong, r.URL.Path)
			}
			http.Error(w, "model unavailable", http.StatusNotFound)
			return
		}
		var part any
		if bytes.Contains(body, []byte("unproductive_state_confidence")) {
			checks++
			confidence := 0.1
			if checks == 1 {
				confidence = 0.95
			} // Exercise the second-model confirmation too.
			part = map[string]any{"text": fmt.Sprintf(`{"unproductive_state_analysis":"Reading different files makes progress.","unproductive_state_confidence":%g}`, confidence)}
		} else {
			turns++
			part = map[string]any{"text": "Review complete."}
			if turns <= 32 {
				part = map[string]any{"functionCall": map[string]any{"name": "read_file", "args": map[string]any{"file_path": filepath.Join(workspace, fmt.Sprintf("sample-%d.txt", turns))}}}
			}
		}
		response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5}, "modelVersion": "gemini-3.8-flash-high"}
		raw, _ := json.Marshal(response)
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", raw)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
		}
	}))
	defer model.Close()
	a := New(cli, func(protocol.StreamEvent) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "loop-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "loop-thread", Workspace: workspace, Prompt: "Read each sample file then finish", Model: "gemini-3.8-flash-high", ApprovalMode: "yolo"}, []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")})
	mu.Lock()
	defer mu.Unlock()
	if err != nil || turns != 33 || checks < 2 || len(wrong) > 0 {
		t.Fatalf("err=%v turns=%d checks=%d wrong models=%v", err, turns, checks, wrong)
	}
}
