package claudeadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

// This opt-in test runs the real CLI against a local Anthropic API stub. It
// never uses an account credential or calls a real model.
func TestRealClaudeCLIWithLocalAPI(t *testing.T) {
	cli := os.Getenv("WIO_CLAUDE_TEST_EXECUTABLE")
	if cli == "" {
		t.Skip("set WIO_CLAUDE_TEST_EXECUTABLE to a Claude Code executable")
	}
	if runtime.GOOS == "windows" {
		// Windows' 8.3 short TEMP path is treated as suspicious by Claude's
		// path safety checks; use the equivalent long path for the fixture.
		longTemp := filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local", "Temp")
		if info, err := os.Stat(longTemp); err == nil && info.IsDir() {
			t.Setenv("TMP", longTemp)
			t.Setenv("TEMP", longTemp)
		}
	}
	workspace := t.TempDir()
	filePath := filepath.Join(workspace, "example.txt")
	if err := os.WriteFile(filePath, []byte("local-read-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		request := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_local\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
		if request == 1 {
			input, _ := json.Marshal(map[string]string{"file_path": filePath})
			partial, _ := json.Marshal(string(input))
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_local\",\"name\":\"Read\",\"input\":{}}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%s}}\n\n", partial)
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from local API.\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	home := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, "config"),
		"ANTHROPIC_API_KEY": "local-test-only", "ANTHROPIC_AUTH_TOKEN": "", "ANTHROPIC_BASE_URL": server.URL,
		"CLAUDE_CODE_OAUTH_TOKEN": "", "CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "", "CLAUDE_CODE_USE_FOUNDRY": "", "ANTHROPIC_CUSTOM_HEADERS": "",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1", "DISABLE_AUTOUPDATER": "1",
		"HTTP_PROXY": "http://127.0.0.1:1", "HTTPS_PROXY": "http://127.0.0.1:1", "ALL_PROXY": "http://127.0.0.1:1", "NO_PROXY": "127.0.0.1,localhost",
		"http_proxy": "http://127.0.0.1:1", "https_proxy": "http://127.0.0.1:1", "all_proxy": "http://127.0.0.1:1", "no_proxy": "127.0.0.1,localhost", "CI": "1",
	} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := protocol.ClaudeTurnCommand{ThreadID: "local", Workspace: workspace, Prompt: "Say hello.", Model: "sonnet", PermissionMode: "default"}
	var text, session string
	var toolUse, toolResult bool
	var toolDetails []string
	run := func() {
		t.Helper()
		err := Run(ctx, cli, "local-op", command, func(event protocol.StreamEvent) error {
			var data struct {
				Text      string `json:"text"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(event.Payload, &data); err != nil {
				return err
			}
			text += data.Text
			toolUse = toolUse || event.Kind == "claude.tool_use"
			toolResult = toolResult || (event.Kind == "claude.tool_result" && strings.Contains(string(event.Payload), "local-read-marker"))
			if event.Kind == "claude.tool_use" || event.Kind == "claude.tool_result" {
				toolDetails = append(toolDetails, event.Kind+" "+string(event.Payload))
			}
			if data.SessionID != "" {
				session = data.SessionID
			}
			return nil
		})
		if err != nil {
			t.Fatalf("CLI failed with local API after %d requests: %v", requests.Load(), err)
		}
		if text != "Hello from local API." || session == "" {
			t.Fatalf("unexpected CLI stream: text=%q session=%q", text, session)
		}
	}
	run()
	if !toolUse || !toolResult {
		t.Fatalf("missing real CLI tool events: use=%v result=%v events=%v", toolUse, toolResult, toolDetails)
	}
	command.SessionID = session
	command.Prompt = "Say hello again."
	text = ""
	run()
	if requests.Load() < 2 {
		t.Fatalf("expected at least two model requests, got %d", requests.Load())
	}
}
