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

	"github.com/wio-platform/wio/internal/protocol"
)

// Exercise the real native tool, permission callback, stdin response and model
// continuation against loopback only. Never use an account or a paid model.
func TestRealClaudeQuestions(t *testing.T) {
	cli := os.Getenv("WIO_CLAUDE_TEST_EXECUTABLE")
	if cli == "" {
		t.Skip("requires an isolated Claude CLI")
	}
	for _, mode := range []string{"default", "plan", "acceptEdits", "bypassPermissions"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			var requests atomic.Int32
			var resumed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/messages") {
					http.NotFound(w, r)
					return
				}
				raw, _ := io.ReadAll(r.Body)
				n := requests.Add(1)
				if n > 1 {
					resumed.Store(strings.Contains(string(raw), "PostgreSQL") && strings.Contains(string(raw), "Tests") && strings.Contains(string(raw), "Docs") && strings.Contains(string(raw), "custom-marker"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_local\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
				stop := "end_turn"
				if n == 1 {
					stop = "tool_use"
					input := `{"questions":[{"question":"Which database?","header":"Database","options":[{"label":"PostgreSQL","description":"SQL"},{"label":"SQLite","description":"Embedded"}],"multiSelect":false},{"question":"Which deliverables?","header":"Output","options":[{"label":"Tests","description":"Verify"},{"label":"Docs","description":"Explain"}],"multiSelect":true},{"question":"Any constraints?","header":"Constraints","options":[{"label":"Fast","description":"Speed"},{"label":"Small","description":"Size"}],"multiSelect":false}]}`
					encoded, _ := json.Marshal(input)
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_question\",\"name\":\"AskUserQuestion\",\"input\":{}}}\n\n")
					fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%s}}\n\n", encoded)
				} else {
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Continued after answer.\"}}\n\n")
				}
				fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q,\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", stop)
			}))
			defer server.Close()
			for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, "config"), "ANTHROPIC_API_KEY": "synthetic-local-only", "ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_AUTH_TOKEN": "", "CLAUDE_CODE_OAUTH_TOKEN": "", "ANTHROPIC_CUSTOM_HEADERS": "", "CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "", "CLAUDE_CODE_USE_FOUNDRY": "", "DISABLE_AUTOUPDATER": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1", "HTTP_PROXY": "http://127.0.0.1:1", "HTTPS_PROXY": "http://127.0.0.1:1", "ALL_PROXY": "http://127.0.0.1:1", "NO_PROXY": "127.0.0.1,localhost", "http_proxy": "http://127.0.0.1:1", "https_proxy": "http://127.0.0.1:1", "all_proxy": "http://127.0.0.1:1", "no_proxy": "127.0.0.1,localhost", "CI": "1"} {
				t.Setenv(key, value)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			var sessions Sessions
			answered := make(chan error, 1)
			var questions, receipts atomic.Int32
			err := sessions.Run(ctx, cli, "native-question-op", protocol.ClaudeTurnCommand{ThreadID: "thread", Workspace: home, Prompt: "Ask me to choose the database and deliverables before proceeding.", Model: "sonnet", PermissionMode: mode, Steer: true, Questions: true}, func(e protocol.StreamEvent) error {
				if e.Kind == "claude.question" {
					questions.Add(1)
					go func() {
						time.Sleep(100 * time.Millisecond)
						if requests.Load() != 1 {
							answered <- fmt.Errorf("CLI continued before answer")
							return
						}
						answered <- sessions.Answer(ctx, protocol.ClaudeAnswerCommand{ThreadID: "thread", OperationID: "native-question-op", RequestID: e.EventID, Answers: map[string]protocol.UserInputAnswer{"0": {Answers: []string{"PostgreSQL"}}, "1": {Answers: []string{"Tests", "Docs"}}, "2": {Answers: []string{"custom-marker"}}}})
					}()
				}
				if e.Kind == "claude.question.answered" {
					receipts.Add(1)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("native question failed after %d requests, %d questions: %v", requests.Load(), questions.Load(), err)
			}
			if questions.Load() != 1 || receipts.Load() != 1 || !resumed.Load() {
				t.Fatalf("missing native question continuation: questions=%d receipts=%d resumed=%v", questions.Load(), receipts.Load(), resumed.Load())
			}
			if err := <-answered; err != nil {
				t.Fatal(err)
			}
		})
	}
}
