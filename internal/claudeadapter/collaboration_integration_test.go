package claudeadapter

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

// The real CLI discovers and invokes the injected MCP against a loopback model
// stub. The isolated home and blocked external proxies avoid account access.
func TestRealClaudeCollaboration(t *testing.T) {
	cli := os.Getenv("WIO_CLAUDE_TEST_EXECUTABLE")
	if cli == "" {
		t.Skip("set WIO_CLAUDE_TEST_EXECUTABLE to an isolated real CLI")
	}
	home := t.TempDir()
	var calls atomic.Int32
	var received atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
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
		if !strings.Contains(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &body)
		tool := ""
		for _, candidate := range body.Tools {
			if strings.HasSuffix(candidate.Name, "__wio_get_context") {
				tool = candidate.Name
			}
		}
		done := strings.Contains(string(raw), "WIO_COLLABORATION_RESULT")
		if done {
			received.Store(true)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(kind string, data any) {
			encoded, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		}
		event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_local", "type": "message", "role": "assistant", "model": "claude-sonnet-4-5", "content": []any{}, "usage": map[string]int{"input_tokens": 5, "output_tokens": 1}}})
		stop := "end_turn"
		if !done && tool != "" {
			stop = "tool_use"
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_collaboration", "name": tool, "input": map[string]any{}}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": "{}"}})
		} else {
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "Local test finished"}})
		}
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 5}})
		event("message_stop", map[string]string{"type": "message_stop"})
	}))
	defer server.Close()
	for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, "config"), "ANTHROPIC_API_KEY": "local-test-only", "ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_AUTH_TOKEN": "", "CLAUDE_CODE_OAUTH_TOKEN": "", "ANTHROPIC_CUSTOM_HEADERS": "", "CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "", "CLAUDE_CODE_USE_FOUNDRY": "", "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1", "HTTP_PROXY": "http://127.0.0.1:1", "HTTPS_PROXY": "http://127.0.0.1:1", "ALL_PROXY": "http://127.0.0.1:1", "NO_PROXY": "127.0.0.1,localhost", "http_proxy": "http://127.0.0.1:1", "https_proxy": "http://127.0.0.1:1", "all_proxy": "http://127.0.0.1:1", "no_proxy": "127.0.0.1,localhost", "CI": "1"} {
		t.Setenv(key, value)
	}
	err = Run(ctx, cli, "collaboration-op", protocol.ClaudeTurnCommand{ThreadID: "collaboration", Workspace: home, Prompt: "Use Wio context", Model: "sonnet", PermissionMode: "default", CollaborationMCP: bridge}, func(protocol.StreamEvent) error { return nil })
	if err != nil || calls.Load() != 1 || !received.Load() {
		t.Fatalf("real MCP round trip: calls=%d received=%v err=%v", calls.Load(), received.Load(), err)
	}
}
