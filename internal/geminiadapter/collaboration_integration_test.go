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
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/mcpbridge"
	"github.com/wio-platform/wio/internal/protocol"
	"github.com/wio-platform/wio/internal/sessiontools"
)

func TestNativeGeminiCollaboration(t *testing.T) {
	cli := os.Getenv("WIO_GEMINI_V066_CLI")
	if cli == "" {
		t.Skip("set WIO_GEMINI_V066_CLI to an isolated real CLI")
	}
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"general":{"disableAutoUpdate":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var calls atomic.Int32
	var received atomic.Bool
	bridge, closeBridge, err := mcpbridge.Start(ctx, func(_ context.Context, name string, _ json.RawMessage) (sessiontools.Reply, error) {
		if name != "wio_get_context" {
			return sessiontools.Reply{}, fmt.Errorf("unexpected tool: %s", name)
		}
		calls.Add(1)
		return sessiontools.Reply{Status: "succeeded", Data: map[string]string{"marker": "WIO_COLLABORATION_RESULT"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeBridge()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "countTokens") {
			io.WriteString(w, `{"totalTokens":10}`)
			return
		}
		if !strings.Contains(r.URL.Path, "generateContent") && !strings.Contains(r.URL.Path, "streamGenerateContent") {
			io.WriteString(w, `{"models":[]}`)
			return
		}
		var body struct {
			Tools []struct {
				Functions []struct {
					Name string `json:"name"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &body)
		tool := ""
		for _, group := range body.Tools {
			for _, candidate := range group.Functions {
				if strings.HasSuffix(candidate.Name, "wio_get_context") {
					tool = candidate.Name
				}
			}
		}
		done := strings.Contains(string(raw), "WIO_COLLABORATION_RESULT")
		if done {
			received.Store(true)
		}
		var part any = map[string]string{"text": "Local test finished"}
		if !done && tool != "" {
			part = map[string]any{"functionCall": map[string]any{"name": tool, "args": map[string]any{}}}
		}
		result := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "modelVersion": "gemini-2.5-flash"}
		encoded, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}))
	defer server.Close()
	environment := []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=local-test-only", "GOOGLE_GEMINI_BASE_URL=" + server.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json"), "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1", "NO_PROXY=127.0.0.1,localhost", "http_proxy=http://127.0.0.1:1", "https_proxy=http://127.0.0.1:1", "all_proxy=http://127.0.0.1:1", "no_proxy=127.0.0.1,localhost"}
	a := New(cli, func(e protocol.StreamEvent) error {
		if e.Kind == "gemini.question" || e.Kind == "gemini.tool_use" {
			t.Logf("%s: %s", e.Kind, e.Payload)
		}
		return nil
	})
	err = a.RunInTrustedWorkspaceWithEnvironment(ctx, "collaboration-op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "collaboration", Workspace: workspace, Prompt: "Use Wio context", Model: "gemini-2.5-flash", ApprovalMode: "default", CollaborationMCP: bridge}, environment)
	if err != nil || calls.Load() != 1 || !received.Load() {
		t.Fatalf("real MCP round trip: calls=%d received=%v err=%v", calls.Load(), received.Load(), err)
	}
}
