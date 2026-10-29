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

func TestGeminiLongTurnCanBeInterruptedWhileWaitingForInput(t *testing.T) {
	t.Setenv("WIO_AGENT_GEMINI_INPUT_CHILD", "1")
	for _, mode := range []string{"parent-cancel", "user-interrupt"} {
		t.Run(mode, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := t.TempDir()
			client := NewClient(Config{StateDir: t.TempDir(), ScanRoots: []string{root}, CloneRoot: root}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			executable, _ := os.Executable()
			if err := client.gemini.ReconfigureCommand(executable, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			command, _ := json.Marshal(protocol.GeminiTurnCommand{Interactive: true, ThreadID: "thread", Workspace: root, Prompt: "Question please", Model: "flash", ApprovalMode: "default"})
			done := make(chan struct{})
			go func() {
				defer close(done)
				client.handleOperation(parent, &protocol.ControlEnvelope{Kind: "gemini.turn.start", OperationID: "turn", PayloadJSON: command})
			}()
			var question protocol.StreamEvent
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for question.Kind != "gemini.question" {
				select {
				case envelope := <-client.outbound:
					if envelope.Kind == "gemini_event" {
						_ = json.Unmarshal(envelope.PayloadJSON, &question)
					}
				case <-deadline.C:
					t.Fatal("turn did not request input")
				}
			}
			if mode == "parent-cancel" {
				cancel()
			} else {
				interrupt, _ := json.Marshal(protocol.GeminiInterruptCommand{ThreadID: "thread", OperationID: "turn"})
				client.handleOperation(parent, &protocol.ControlEnvelope{Kind: "gemini.turn.interrupt", OperationID: "stop", PayloadJSON: interrupt})
				client.seenMu.Lock()
				result := client.seen["stop"].result
				client.seenMu.Unlock()
				var confirmation protocol.GeminiInterruptResult
				if result.Status != "succeeded" || json.Unmarshal(result.Data, &confirmation) != nil || !confirmation.Stopped || confirmation.OperationID != "turn" {
					t.Fatalf("interrupt did not confirm process exit: %+v", result)
				}
			}
			select {
			case <-done:
			case <-deadline.C:
				t.Fatal("cancelled turn kept running")
			}
			client.seenMu.Lock()
			result := client.seen["turn"].result
			client.seenMu.Unlock()
			if result.Status != "failed" || result.Message != context.Canceled.Error() {
				t.Fatalf("cancelled turn did not finish: %#v", result)
			}
			if err := client.gemini.Answer(protocol.GeminiAnswerCommand{ThreadID: "thread", OperationID: "turn", RequestID: question.EventID, Answers: map[string]protocol.UserInputAnswer{"q": {Answers: []string{"Yes"}}}}); err == nil {
				t.Fatal("cancelled question still accepted an answer")
			}
			if err := client.gemini.ReconfigureCommand(executable, func() error { return nil }); err != nil {
				t.Fatal("cancelled turn did not release adapter")
			}
		})
	}
}

func TestGeminiInterruptRequiresOwnedExecutionOrCompletedReceipt(t *testing.T) {
	client := NewClient(Config{StateDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := client.interruptGeminiAndWait(context.Background(), "orphan"); err == nil {
		t.Fatal("unknown execution was reported stopped after an Agent restart")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	client.seen["starting"] = &operationExecution{kind: "gemini.turn.start", cancel: cancel, done: done}
	ack := make(chan error, 1)
	go func() { ack <- client.interruptGeminiAndWait(context.Background(), "starting") }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("a turn not yet registered in the adapter could not be cancelled")
	}
	select {
	case <-ack:
		t.Fatal("interruption confirmed before execution cleanup")
	default:
	}
	close(done)
	if err := <-ack; err != nil {
		t.Fatal(err)
	}
	if err := client.interruptGeminiAndWait(context.Background(), "starting"); err != nil {
		t.Fatal("just-completed execution was not recognized", err)
	}
	if err := client.saveOperationResult(protocol.OperationResult{OperationID: "finished", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := client.interruptGeminiAndWait(context.Background(), "finished"); err != nil {
		t.Fatal("persisted completion was not recognized", err)
	}
}
