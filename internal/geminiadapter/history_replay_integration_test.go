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
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestNativeGeminiResumeDoesNotRepeatHistory(t *testing.T) {
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
	const answer = "The same answer is legitimate."
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.Contains(r.URL.Path, "countTokens") {
			_, _ = io.WriteString(w, `{"totalTokens":10}`)
			return
		}
		if !strings.Contains(r.URL.Path, "generateContent") && !strings.Contains(r.URL.Path, "streamGenerateContent") {
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": answer}}}, "finishReason": "STOP"}}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var session string
	var replies []string
	a := New(cli, func(event protocol.StreamEvent) error {
		var payload struct {
			Session string `json:"session_id"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		if event.Kind == "gemini.init" {
			session = payload.Session
		}
		if event.Kind == "gemini.message" {
			replies = append(replies, payload.Content)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	environment := []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")}
	command := protocol.GeminiTurnCommand{Interactive: true, ThreadID: "history-thread", Workspace: workspace, Prompt: "First prompt", Model: "gemini-2.5-flash", ApprovalMode: "default"}
	if err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "first", command, environment); err != nil {
		t.Fatal(err)
	}
	if session == "" {
		t.Fatal("native session missing")
	}
	command.SessionID = session
	history := newNativeSearchHistory(session, workspace, environment, true)
	if history == nil {
		t.Fatal("native history missing")
	}
	// Seed a large replay with thoughts and terminal tool snapshots, ensuring
	// commands-ready can race the replay. No actual tools or paid models run.
	file, err := os.OpenFile(history.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for i := 0; i < 80; i++ {
		for _, message := range []map[string]any{
			{"id": fmt.Sprintf("u%d", i), "type": "user", "content": []any{map[string]string{"text": "old prompt"}}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
			{"id": fmt.Sprintf("m%d", i), "type": "gemini", "content": []any{map[string]string{"text": "OLD_REPLAY_ONLY"}}, "thoughts": []any{map[string]string{"subject": "old", "description": "old thought"}}, "toolCalls": []any{map[string]any{"id": fmt.Sprintf("read_file__old%d", i), "name": "read_file", "args": map[string]string{"file_path": "old.txt"}, "status": "success", "resultDisplay": "old output"}}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)},
		} {
			if err := encoder.Encode(message); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		replies = nil
		command.Prompt = "Please repeat your answer"
		if err := a.RunInTrustedWorkspaceWithEnvironment(ctx, fmt.Sprintf("resume%d", i), command, environment); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(replies, ""); got != answer {
			t.Fatalf("resume leaked history or lost genuine repeated answer: %q", got)
		}
	}
}
