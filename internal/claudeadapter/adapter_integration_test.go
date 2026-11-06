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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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
	var bodyMu sync.Mutex
	var lastBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		request := requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		bodyMu.Lock()
		lastBody = string(body)
		bodyMu.Unlock()
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	command := protocol.ClaudeTurnCommand{ThreadID: "local", Workspace: workspace, Prompt: "Say hello.", Model: "sonnet", PermissionMode: "default"}
	var text, session string
	var toolUse, toolResult bool
	var toolDetails []string
	operationID := uuid.NewString()
	firstID := operationID
	run := func() {
		t.Helper()
		var resultID string
		err := Run(ctx, cli, operationID, command, func(event protocol.StreamEvent) error {
			var data struct {
				Text      string `json:"text"`
				SessionID string `json:"session_id"`
				ResultID  string `json:"result_id"`
				Usage     struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(event.Payload, &data); err != nil {
				return err
			}
			text += data.Text
			if event.Kind == "claude.result" {
				resultID = data.ResultID
				if resultID == "" || data.Usage.InputTokens <= 0 || data.Usage.OutputTokens <= 0 {
					return fmt.Errorf("native result identifier or token usage missing: %s", event.Payload)
				}
			}
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
		if text != "Hello from local API." || session == "" || resultID == "" {
			t.Fatalf("unexpected CLI stream: text=%q session=%q", text, session)
		}
	}
	run()
	if !toolUse || !toolResult {
		t.Fatalf("missing real CLI tool events: use=%v result=%v events=%v", toolUse, toolResult, toolDetails)
	}
	command.SessionID = session
	command.Prompt = "Say hello again."
	operationID = uuid.NewString()
	editID := operationID
	text = ""
	run()
	if requests.Load() < 2 {
		t.Fatalf("expected at least two model requests, got %d", requests.Load())
	}
	originalSession := session
	state := t.TempDir()
	command.Rewrite = &protocol.ClaudeRewriteTarget{MessageID: editID, Prompt: command.Prompt}
	command.Prompt = "Revised second prompt."
	operationID = uuid.NewString()
	if err := PrepareRewrite(ctx, state, operationID, &command); err != nil {
		t.Fatal(err)
	}
	if command.ResumeAt == "" {
		t.Fatal("missing assistant resume boundary")
	}
	text = ""
	run()
	bodyMu.Lock()
	body := lastBody
	bodyMu.Unlock()
	if session == originalSession || !strings.Contains(body, "Say hello.") || !strings.Contains(body, command.Prompt) || strings.Contains(body, "Say hello again.") || strings.Count(body, "Hello from local API.") != 1 {
		t.Fatalf("native rewrite did not fork the correct prefix: %s", body)
	}
	// The original session still contains the discarded prompt.
	command.Rewrite = nil
	command.ResumeAt = ""
	command.ResumePath = ""
	command.SessionID = originalSession
	command.Prompt = "Continue source."
	text = ""
	run()
	bodyMu.Lock()
	body = lastBody
	bodyMu.Unlock()
	if !strings.Contains(body, "Say hello again.") {
		t.Fatal("rewrite changed the source transcript")
	}
	command.Rewrite = &protocol.ClaudeRewriteTarget{MessageID: firstID}
	command.Prompt = "Revised first prompt."
	operationID = uuid.NewString()
	if err := PrepareRewrite(ctx, state, operationID, &command); err != nil {
		t.Fatal(err)
	}
	if command.ResumeAt != "" || command.ResumePath != "" {
		t.Fatal("first message should start with an empty prefix")
	}
	text = ""
	run()
	bodyMu.Lock()
	body = lastBody
	bodyMu.Unlock()
	if strings.Contains(body, "Say hello.") || strings.Contains(body, "Say hello again.") || strings.Contains(body, "Continue source.") || !strings.Contains(body, command.Prompt) {
		t.Fatalf("first edit retained old history: %s", body)
	}
}
