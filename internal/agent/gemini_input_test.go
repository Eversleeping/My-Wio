package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func init() {
	if os.Getenv("WIO_AGENT_GEMINI_INPUT_CHILD") == "" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var bridge struct {
		URL     string `json:"url"`
		Headers []struct {
			Value string `json:"value"`
		} `json:"headers"`
	}
	for {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decoder.Decode(&msg) != nil {
			os.Exit(0)
		}
		result := map[string]any{}
		if msg.Method == "session/new" {
			var p struct {
				Servers []json.RawMessage `json:"mcpServers"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			_ = json.Unmarshal(p.Servers[0], &bridge)
			result["sessionId"] = "native"
		}
		if msg.Method == "session/prompt" {
			request, _ := http.NewRequest("POST", bridge.URL, strings.NewReader(`{"id":1,"method":"tools/call","params":{"name":"wio_ask_user","arguments":{"questions":[{"id":"q","question":"Continue?"}]}}}`))
			request.Header.Set("Authorization", bridge.Headers[0].Value)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				os.Exit(2)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if !strings.Contains(string(body), "Yes") {
				os.Exit(3)
			}
			result["stopReason"] = "end_turn"
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}
}

func TestGeminiQuestionEventIdentitySurvivesAgentDispatch(t *testing.T) {
	t.Setenv("WIO_AGENT_GEMINI_INPUT_CHILD", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	client := NewClient(Config{StateDir: t.TempDir(), ScanRoots: []string{root}, CloneRoot: root}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	executable, _ := os.Executable()
	if err := client.gemini.ReconfigureCommand(executable, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	command, _ := json.Marshal(protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: root, Prompt: "Question please", Model: "flash", ApprovalMode: "default"})
	done := make(chan error, 1)
	go func() {
		done <- client.execute(ctx, &protocol.ControlEnvelope{Kind: "gemini.turn.start", OperationID: "turn", PayloadJSON: command})
	}()
	var question protocol.StreamEvent
	for question.Kind != "gemini.question" {
		select {
		case envelope := <-client.outbound:
			if envelope.Kind == "gemini_event" {
				_ = json.Unmarshal(envelope.PayloadJSON, &question)
			}
		case <-ctx.Done():
			t.Fatal("question not emitted")
		}
	}
	answer, _ := json.Marshal(protocol.GeminiAnswerCommand{ThreadID: "thread", OperationID: "turn", RequestID: question.EventID, Answers: map[string]protocol.UserInputAnswer{"q": {Answers: []string{"Yes"}}}})
	if err := client.execute(ctx, &protocol.ControlEnvelope{Kind: "gemini.question.answer", OperationID: "answer", PayloadJSON: answer}); err != nil {
		t.Fatalf("browser-visible question id did not resolve to adapter: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
