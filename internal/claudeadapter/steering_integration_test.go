package claudeadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

// Runs the official CLI against a loopback API stub. No real model is used.
func TestRealClaudeLiveSteering(t *testing.T) {
	t.Run("streaming-text", func(t *testing.T) { realClaudeSteering(t, false, false, "correct-direction-marker") })
	t.Run("running-shell", func(t *testing.T) { realClaudeSteering(t, true, false, "correct-direction-marker") })
	t.Run("image-only", func(t *testing.T) { realClaudeSteering(t, false, true, "") })
	t.Run("text-with-image", func(t *testing.T) { realClaudeSteering(t, false, true, "correct-direction-marker") })
}
func realClaudeSteering(t *testing.T, tool, withImage bool, prompt string) {
	cli := os.Getenv("WIO_CLAUDE_TEST_EXECUTABLE")
	if cli == "" {
		t.Skip("requires an isolated Claude CLI")
	}
	home := t.TempDir()
	const pngData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII="
	var requests atomic.Int32
	var corrected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		n := requests.Add(1)
		matches := prompt == "" || strings.Count(string(body.Messages), "correct-direction-marker") == 1
		if withImage {
			var messages []struct {
				Content []struct {
					Type   string `json:"type"`
					Source struct {
						Type      string `json:"type"`
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source"`
				} `json:"content"`
			}
			_ = json.Unmarshal(body.Messages, &messages)
			images := 0
			for _, message := range messages {
				for _, block := range message.Content {
					if block.Type == "image" && block.Source.Type == "base64" && block.Source.MediaType == "image/png" && block.Source.Data == pngData {
						images++
					}
				}
			}
			matches = matches && images == 1
		}
		if n > 1 && matches {
			corrected.Store(true)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_local\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
		if n == 1 && tool {
			input, _ := json.Marshal(map[string]any{"command": "sleep 2; printf finished > steering-shell-finished.txt", "timeout": 10000})
			encoded, _ := json.Marshal(string(input))
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_wait\",\"name\":\"Bash\",\"input\":{}}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%s}}\n\n", encoded)
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		text := "Initial response"
		if n > 1 {
			text = "Corrected response"
		}
		delta, _ := json.Marshal(text)
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", delta)
		w.(http.Flusher).Flush()
		if n == 1 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(25 * time.Second):
				return
			}
		}
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, "config"), "ANTHROPIC_API_KEY": "synthetic-local-only", "ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_AUTH_TOKEN": "", "CLAUDE_CODE_OAUTH_TOKEN": "", "ANTHROPIC_CUSTOM_HEADERS": "", "CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "", "CLAUDE_CODE_USE_FOUNDRY": "", "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1", "HTTP_PROXY": "http://127.0.0.1:1", "HTTPS_PROXY": "http://127.0.0.1:1", "ALL_PROXY": "http://127.0.0.1:1", "NO_PROXY": "127.0.0.1,localhost", "http_proxy": "http://127.0.0.1:1", "https_proxy": "http://127.0.0.1:1", "all_proxy": "http://127.0.0.1:1", "no_proxy": "127.0.0.1,localhost", "CI": "1"} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var sessions Sessions
	var once sync.Once
	steered := make(chan error, 1)
	var kinds []string
	var texts string
	command := protocol.ClaudeSteerCommand{ThreadID: "live", OperationID: "live-op", RequestID: uuid.NewString(), Prompt: prompt}
	if withImage {
		command.Images = []protocol.TurnImage{{DataURL: "data:image/png;base64," + pngData}}
	}
	mode := "default"
	if tool {
		mode = "bypassPermissions"
	}
	err := sessions.Run(ctx, cli, "live-op", protocol.ClaudeTurnCommand{ThreadID: "live", Workspace: home, Prompt: "Initial request", Model: "sonnet", PermissionMode: mode, Steer: true}, func(e protocol.StreamEvent) error {
		kinds = append(kinds, e.Kind)
		if e.Kind == "claude.message" {
			var payload struct{ Text string }
			_ = json.Unmarshal(e.Payload, &payload)
			texts += payload.Text
			if !tool {
				once.Do(func() { go func() { steered <- sessions.Steer(ctx, command) }() })
			}
		}
		if tool && e.Kind == "claude.tool_use" {
			once.Do(func() { go func() { time.Sleep(200 * time.Millisecond); steered <- sessions.Steer(ctx, command) }() })
		}
		return nil
	})
	if err != nil {
		t.Fatalf("live CLI failed: %v; requests=%d kinds=%v", err, requests.Load(), kinds)
	}
	select {
	case err := <-steered:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("no steer receipt")
	}
	if !corrected.Load() || !strings.Contains(texts, "Corrected response") {
		t.Fatalf("correction not in native request: requests=%d corrected=%v text=%q events=%v", requests.Load(), corrected.Load(), texts, kinds)
	}
	if tool {
		if _, err := os.Stat(filepath.Join(home, "steering-shell-finished.txt")); err != nil {
			t.Fatalf("released task before background shell finished: %v events=%v", err, kinds)
		}
	}
	joined := strings.Join(kinds, ",")
	if strings.Count(joined, "claude.steer.accepted") != 1 || strings.Count(joined, "claude.steer.applied") != 1 {
		t.Fatalf("incorrect receipts: %v", kinds)
	}
}
