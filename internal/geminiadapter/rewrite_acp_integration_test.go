package geminiadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

func TestNativeGeminiACPRewriteUsesRetainedPrefix(t *testing.T) {
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
	var mu sync.Mutex
	requests := 0
	var rewriteBody []byte
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "countTokens") {
			_, _ = io.WriteString(w, `{"totalTokens":10}`)
			return
		}
		if !strings.Contains(r.URL.Path, "generateContent") && !strings.Contains(r.URL.Path, "streamGenerateContent") {
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		mu.Lock()
		requests++
		n := requests
		if n == 3 {
			rewriteBody = append([]byte(nil), body...)
		}
		mu.Unlock()
		response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": fmt.Sprintf("NATIVE_RESPONSE_%d", n)}}}, "finishReason": "STOP"}}, "modelVersion": "gemini-2.5-flash"}
		raw, _ := json.Marshal(response)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer model.Close()
	var events []protocol.StreamEvent
	a := New(cli, func(event protocol.StreamEvent) error { events = append(events, event); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	environment := []string{"GEMINI_CLI_HOME=" + home, "GEMINI_API_KEY=fake-local-test-key", "GOOGLE_GEMINI_BASE_URL=" + model.URL, "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(home, "missing.json"), "GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(home, "missing-defaults.json")}
	command := protocol.GeminiTurnCommand{Interactive: true, ThreadID: "rewrite-thread", Workspace: workspace, Prompt: "FIRST_CONTEXT", Model: "gemini-2.5-flash", ApprovalMode: "default"}
	if err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "first", command, environment); err != nil {
		t.Fatal(err)
	}
	getInit := func(operation string) (session, timestamp string) {
		for _, event := range events {
			if event.Kind != "gemini.init" {
				continue
			}
			var payload struct {
				OperationID string `json:"operation_id"`
				SessionID   string `json:"session_id"`
				Timestamp   string `json:"timestamp"`
			}
			_ = json.Unmarshal(event.Payload, &payload)
			if payload.OperationID == operation {
				return payload.SessionID, payload.Timestamp
			}
		}
		return "", ""
	}
	command.SessionID, _ = getInit("first")
	if command.SessionID == "" {
		t.Fatal("first native session missing")
	}
	command.Prompt = "SECOND_DISCARD"
	if err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "second", command, environment); err != nil {
		t.Fatal(err)
	}
	_, secondStarted := getInit("second")
	if secondStarted == "" {
		t.Fatal("second native turn missing")
	}
	history := newNativeSearchHistory(command.SessionID, workspace, environment, true)
	if history == nil {
		t.Fatal("original native history missing")
	}
	original, err := os.ReadFile(history.path)
	if err != nil {
		t.Fatal(err)
	}
	command.Prompt = "NEW_REPLACEMENT"
	command.Rewrite = &protocol.GeminiRewriteTarget{EventID: "second-user", StartedAt: secondStarted}
	if err := a.RunInTrustedWorkspaceWithEnvironment(ctx, "rewrite", command, environment); err != nil {
		t.Fatal(err)
	}
	replacementID, _ := getInit("rewrite")
	if replacementID == "" || replacementID == command.SessionID {
		t.Fatalf("rewrite did not load a new native session: %q", replacementID)
	}
	current, err := os.ReadFile(history.path)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("rewrite changed the original native history")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 3 || !bytes.Contains(rewriteBody, []byte("FIRST_CONTEXT")) || !bytes.Contains(rewriteBody, []byte("NATIVE_RESPONSE_1")) || !bytes.Contains(rewriteBody, []byte("NEW_REPLACEMENT")) || bytes.Contains(rewriteBody, []byte("SECOND_DISCARD")) || bytes.Contains(rewriteBody, []byte("NATIVE_RESPONSE_2")) {
		t.Fatalf("rewritten model context contains the wrong history: requests=%d", requests)
	}
}
