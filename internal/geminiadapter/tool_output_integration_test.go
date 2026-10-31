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

// This uses the real CLI with a local model, so no credentials or inference
// leave the machine. It distinguishes missing ACP display data from the
// search result actually supplied to the model.
func TestNativeGeminiSearchToolOutput(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("UNMATCHED_CONTEXT_SHOULD_NOT_BE_DISPLAYED\nsearch evidence: WIO_SEARCH_MARKER\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	resultDelivered := false
	noMatchDelivered := false
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
		if bytes.Contains(body, []byte(`"functionResponse"`)) && bytes.Contains(body, []byte("search evidence: WIO_SEARCH_MARKER")) {
			resultDelivered = true
		}
		if bytes.Contains(body, []byte(`"functionResponse"`)) && bytes.Contains(body, []byte("No matches found")) {
			noMatchDelivered = true
		}
		mu.Unlock()
		var part any = map[string]any{"text": "Search completed."}
		if n == 1 {
			part = map[string]any{"functionCall": map[string]any{"name": "grep_search", "args": map[string]any{"pattern": "WIO_SEARCH_MARKER", "dir_path": workspace}}}
		} else if n == 3 {
			part = map[string]any{"functionCall": map[string]any{"name": "grep_search", "args": map[string]any{"pattern": "WIO_SEARCH_NO_MATCH", "dir_path": workspace}}}
		}
		response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var events []protocol.StreamEvent
	a := New(cli, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	environment := []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")}
	command := protocol.GeminiTurnCommand{Interactive: true, ThreadID: "search-thread", Workspace: workspace, Prompt: "Search for the marker", Model: "gemini-2.5-flash", ApprovalMode: "yolo"}
	err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "search-op", command, environment)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "gemini.init" {
			var payload struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(event.Payload, &payload)
			command.SessionID = payload.SessionID
		}
	}
	if command.SessionID == "" {
		t.Fatal("native session not initialized")
	}
	command.Prompt = "Search for an absent marker"
	if err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "search-resume", command, environment); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests < 4 || !resultDelivered || !noMatchDelivered {
		t.Fatalf("search results did not reach the local model: requests=%d match=%v noMatch=%v events=%+v", requests, resultDelivered, noMatchDelivered, events)
	}
	var found, missing bool
	for _, event := range events {
		if event.Kind == "gemini.tool_result" && bytes.Contains(event.Payload, []byte("grep_search")) {
			if bytes.Contains(event.Payload, []byte("UNMATCHED_CONTEXT_SHOULD_NOT_BE_DISPLAYED")) {
				t.Fatal("recovery exposed model context outside the CLI's intended display matches")
			}
			found = found || bytes.Contains(event.Payload, []byte("search evidence: WIO_SEARCH_MARKER"))
			missing = missing || bytes.Contains(event.Payload, []byte("No matches found"))
		}
	}
	if !found || !missing {
		t.Fatalf("search outputs were not preserved in the events: %+v", events)
	}
}
