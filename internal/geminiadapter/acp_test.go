package geminiadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func fakeACPCLI() {
	mode := "default"
	for i, arg := range os.Args {
		if arg == "--approval-mode" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if override := os.Getenv("WIO_GEMINI_TEST_MODE_OVERRIDE"); override != "" {
		mode = override
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var bridge struct {
		URL     string `json:"url"`
		Name    string `json:"name"`
		Headers []struct {
			Value string `json:"value"`
		} `json:"headers"`
	}
	notify := func(update any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-acp", "update": update}})
	}
	for {
		var msg acpMessage
		if decoder.Decode(&msg) != nil {
			return
		}
		var result any = map[string]any{}
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1}
		case "session/new", "session/load":
			var p struct {
				Servers []json.RawMessage `json:"mcpServers"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			_ = json.Unmarshal(p.Servers[0], &bridge)
			result = map[string]any{"sessionId": "native-acp", "modes": map[string]string{"currentModeId": mode}}
			if msg.Method == "session/load" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
				notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "OLD_REPLAY_MUST_NOT_APPEAR"}})
				notify(map[string]string{"sessionUpdate": "available_commands_update"})
				// Command availability does not fence asynchronous history replay.
				for _, status := range []string{"completed", "failed"} {
					notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "OLD_REPLAY_" + status, "title": "Shell", "status": status, "content": []any{}})
				}
				continue
			}
		case "session/prompt":
			_ = os.WriteFile(os.Getenv("WIO_GEMINI_TEST_RECORD"), msg.Params, 0600)
			if text := os.Getenv("WIO_GEMINI_TEST_MODE_TEXT"); text != "" {
				notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}})
				result = map[string]any{"stopReason": "end_turn"}
				break
			}
			if sample := os.Getenv("WIO_GEMINI_TEST_USAGE_RESULT"); sample != "" {
				_ = json.Unmarshal([]byte(sample), &result)
				break
			}
			if os.Getenv("WIO_GEMINI_TEST_PERMISSION") == "1" {
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": "permission-rpc", "method": "session/request_permission", "params": map[string]any{"toolCall": map[string]any{"toolCallId": "shell-permission", "title": "test command", "content": []any{}}, "options": []any{map[string]string{"optionId": "persist", "name": "Allow for this session", "kind": "allow_always"}, map[string]string{"optionId": "once", "name": "Allow", "kind": "allow_once"}, map[string]string{"optionId": "reject", "name": "Reject", "kind": "reject_once"}}}})
				var answer acpMessage
				if decoder.Decode(&answer) != nil {
					return
				}
				_ = os.WriteFile(os.Getenv("WIO_GEMINI_TEST_RECORD"), answer.Result, 0600)
				result = map[string]any{"stopReason": "end_turn"}
				break
			}
			notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "bridge-call", "title": "wio_ask_user()", "status": "pending", "content": []any{}})
			request, _ := http.NewRequest("POST", bridge.URL, strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wio_ask_user","arguments":{"questions":[{"id":"q","question":"Continue?"}]}}}`))
			request.Header.Set("Authorization", bridge.Headers[0].Value)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				os.Exit(2)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if !bytes.Contains(body, []byte("Yes")) {
				os.Exit(3)
			}
			notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "shell-call", "title": "printf ACP_SHELL_OUTPUT", "status": "in_progress", "content": []any{}})
			// Replay can also arrive after a genuine call has started.
			if os.Getenv("WIO_GEMINI_TEST_RESUME") == "1" {
				notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "OLD_REPLAY_LATE", "title": "ReadFile", "status": "completed", "content": []any{}})
			}
			// Model CLI 0.65's ACP conversion: terminal-format results are
			// discarded, while child-process output is delivered as text.
			output := []any{}
			if os.Getenv("GEMINI_PTY_INFO") == "child_process" {
				output = append(output, map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": "ACP_SHELL_OUTPUT"}})
			}
			notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "shell-call", "title": "printf ACP_SHELL_OUTPUT", "status": "completed", "content": output})
			// Validation errors can arrive without a tool_call; keep them visible.
			notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "invalid-call", "status": "failed", "content": []any{}})
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "continued"}})
			result = map[string]any{"stopReason": "end_turn", "usage": map[string]int{"inputTokens": 37, "outputTokens": 4}}
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}
}

func TestACPDistinguishesMissingUsageFromZero(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("WIO_GEMINI_TEST_RECORD", filepath.Join(t.TempDir(), "prompt.json"))
	executable, _ := os.Executable()
	for _, tc := range []struct {
		name, response string
		input, output  *int64
	}{
		{name: "missing", response: `{"stopReason":"end_turn"}`},
		{name: "zero", response: `{"stopReason":"end_turn","usage":{"inputTokens":0,"outputTokens":0}}`, input: int64Ptr(0), output: int64Ptr(0)},
		{name: "quota fallback", response: `{"stopReason":"end_turn","usage":{"inputTokens":0,"outputTokens":0},"_meta":{"quota":{"token_count":{"input_tokens":8,"output_tokens":2}}}}`, input: int64Ptr(8), output: int64Ptr(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WIO_GEMINI_TEST_USAGE_RESULT", tc.response)
			var stats map[string]json.RawMessage
			a := New(executable, func(e protocol.StreamEvent) error {
				if e.Kind == "gemini.result" {
					var p struct {
						Stats map[string]json.RawMessage `json:"stats"`
					}
					_ = json.Unmarshal(e.Payload, &p)
					stats = p.Stats
				}
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.Run(ctx, "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), Prompt: "test", Model: "flash", ApprovalMode: "default"}); err != nil {
				t.Fatal(err)
			}
			for key, expected := range map[string]*int64{"input_tokens": tc.input, "output_tokens": tc.output} {
				var value int64
				raw, present := stats[key]
				if expected == nil && present {
					t.Fatalf("%s should be absent: %s", key, raw)
				}
				if expected != nil && (!present || json.Unmarshal(raw, &value) != nil || value != *expected) {
					t.Fatalf("unexpected %s: %s", key, raw)
				}
			}
		})
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestACPImagesQuestionsAndLateHistoryReplay(t *testing.T) {
	t.Setenv("WIO_GEMINI_TEST_CHILD", "acp")
	t.Setenv("GEMINI_PTY_INFO", "node-pty")
	record := filepath.Join(t.TempDir(), "prompt.json")
	t.Setenv("WIO_GEMINI_TEST_RECORD", record)
	executable, _ := os.Executable()
	for _, session := range []string{"", "native-acp"} {
		t.Run("session-"+session, func(t *testing.T) {
			if session != "" {
				t.Setenv("WIO_GEMINI_TEST_RESUME", "1")
			}
			var events []protocol.StreamEvent
			var a *Adapter
			a = New(executable, func(e protocol.StreamEvent) error {
				events = append(events, e)
				if e.Kind == "gemini.question" {
					go func() {
						_ = a.Answer(protocol.GeminiAnswerCommand{ThreadID: "thread", OperationID: "op", RequestID: e.EventID, Answers: map[string]protocol.UserInputAnswer{"q": {Answers: []string{"Yes"}}}})
					}()
				}
				return nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			image := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="
			err := a.Run(ctx, "op", protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: t.TempDir(), SessionID: session, Prompt: "image prompt", Images: []protocol.TurnImage{{DataURL: "data:image/png;base64," + image}}, Model: "flash", ApprovalMode: "default"})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(record)
			var prompt struct {
				Prompt []struct {
					Type string `json:"type"`
					Text string `json:"text"`
					Data string `json:"data"`
					MIME string `json:"mimeType"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(raw, &prompt)
			if len(prompt.Prompt) != 2 || prompt.Prompt[0].Text != "image prompt" || prompt.Prompt[1].Type != "image" || prompt.Prompt[1].Data != image || prompt.Prompt[1].MIME != "image/png" {
				t.Fatalf("image not native ACP input: %s", raw)
			}
			transcript, _ := json.Marshal(events)
			if bytes.Contains(transcript, []byte("OLD_REPLAY")) || !bytes.Contains(transcript, []byte("continued")) || !bytes.Contains(transcript, []byte(`"input_tokens":37`)) || !bytes.Contains(transcript, []byte(`"tool_calls":2`)) || !bytes.Contains(transcript, []byte("invalid-call")) {
				t.Fatalf("incorrect events: %s", transcript)
			}
			for _, e := range events {
				if e.Kind == "gemini.question" && bytes.Contains(e.Payload, []byte("isSecret")) {
					t.Fatal("false secret flag will be corrupted by redaction")
				}
			}
			var shellOutput bool
			for _, e := range events {
				if e.Kind == "gemini.tool_result" && bytes.Contains(e.Payload, []byte(`"text":"ACP_SHELL_OUTPUT"`)) {
					shellOutput = true
				}
			}
			if !shellOutput {
				t.Fatalf("ACP shell output was lost: %s", transcript)
			}
			if len(a.questions) != 0 {
				t.Fatal(fmt.Sprintf("pending questions leaked: %d", len(a.questions)))
			}
		})
	}
}
