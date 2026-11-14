package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
)

const fakeSessionID = "550e8400-e29b-41d4-a716-446655440007"

var hangPattern = regexp.MustCompile(`P0-07-HANG=([A-Za-z0-9_-]+)`)

func main() {
	engine := filepath.Base(os.Args[0])
	if hasArgument("--version") {
		switch engine {
		case "codex":
			fmt.Println("codex-cli 0.1.0")
		case "gemini":
			fmt.Println("0.66.0")
		default:
			fmt.Println("1.0.0")
		}
		return
	}
	switch engine {
	case "codex":
		runCodex()
	case "claude":
		runClaude()
	case "gemini":
		runGemini()
	default:
		fmt.Fprintf(os.Stderr, "unsupported fake engine name %q\n", engine)
		os.Exit(2)
	}
}

func hasArgument(value string) bool {
	for _, argument := range os.Args[1:] {
		if argument == value {
			return true
		}
	}
	return false
}

func hangMarker(input string) string {
	match := hangPattern.FindStringSubmatch(input)
	if len(match) != 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(match[1])
	if err != nil {
		return ""
	}
	return string(raw)
}

func markStarted(path string) {
	if path != "" {
		_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	}
}

func runClaude() {
	input, _ := io.ReadAll(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": fakeSessionID})
	if marker := hangMarker(string(input)); marker != "" {
		markStarted(marker)
		waitForTermination()
		return
	}
	_ = encoder.Encode(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": "P0-07 fake Claude reply"}}})
	_ = encoder.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]string{{"type": "text", "text": "P0-07 fake Claude reply"}}}})
	_ = encoder.Encode(map[string]any{"type": "result", "uuid": "p0-07-result", "subtype": "success", "is_error": false, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}, "permission_denials": []any{}})
}

func runGemini() {
	if !hasArgument("--acp") {
		fmt.Fprintln(os.Stderr, "fake Gemini supports only the ACP mode used by this acceptance test")
		os.Exit(2)
	}
	runGeminiACP()
}

func runGeminiACP() {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	respond := func(id json.RawMessage, result any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	notify := func(update any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "p0-07-gemini-session", "update": update}})
	}
	for {
		var message rpcMessage
		if decoder.Decode(&message) != nil {
			return
		}
		switch message.Method {
		case "initialize":
			respond(message.ID, map[string]int{"protocolVersion": 1})
		case "session/new":
			respond(message.ID, map[string]any{"sessionId": "p0-07-gemini-session", "modes": map[string]string{"currentModeId": "default"}})
		case "session/load":
			respond(message.ID, map[string]any{"sessionId": "p0-07-gemini-session", "modes": map[string]string{"currentModeId": "default"}, "_meta": map[string]bool{"wioHistoryReplayed": true}})
			notify(map[string]string{"sessionUpdate": "available_commands_update"})
		case "session/prompt":
			if marker := hangMarker(string(message.Params)); marker != "" {
				markStarted(marker)
				waitForTermination()
				return
			}
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "P0-07 fake Gemini reply"}})
			respond(message.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]int{"inputTokens": 1, "outputTokens": 1}})
		}
	}
}

func waitForTermination() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func runCodex() {
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	threadID := "p0-07-codex-thread"
	turnNumber := 0
	activeTurn := ""
	activeMarker := ""
	respond := func(id json.RawMessage, result any) {
		_ = encoder.Encode(map[string]any{"id": id, "result": result})
	}
	notify := func(method string, params any) {
		_ = encoder.Encode(map[string]any{"method": method, "params": params})
	}
	for scanner.Scan() {
		var message rpcMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		switch message.Method {
		case "initialize":
			respond(message.ID, map[string]any{})
		case "initialized":
		case "thread/start":
			respond(message.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "thread/resume":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(message.Params, &params)
			if params.ThreadID != "" {
				threadID = params.ThreadID
			}
			respond(message.ID, map[string]any{"thread": map[string]string{"id": threadID}})
		case "turn/start":
			turnNumber++
			activeTurn = fmt.Sprintf("p0-07-turn-%d", turnNumber)
			respond(message.ID, map[string]any{"turn": map[string]string{"id": activeTurn}})
			notify("turn/started", map[string]any{"threadId": threadID, "turn": map[string]string{"id": activeTurn}})
			if marker := hangMarker(string(message.Params)); marker != "" {
				activeMarker = marker
				markStarted(marker)
				continue
			}
			notify("item/completed", map[string]any{"threadId": threadID, "turnId": activeTurn, "item": map[string]string{"id": fmt.Sprintf("p0-07-item-%d", turnNumber), "type": "agentMessage", "text": "P0-07 fake Codex reply"}})
			notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]string{"id": activeTurn, "status": "completed"}})
			activeTurn = ""
		case "turn/interrupt":
			respond(message.ID, map[string]any{})
			if activeTurn != "" {
				notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]string{"id": activeTurn, "status": "interrupted"}})
				if activeMarker != "" {
					file, err := os.OpenFile(activeMarker, os.O_APPEND|os.O_WRONLY, 0o600)
					if err == nil {
						_, _ = file.WriteString("interrupted\n")
						_ = file.Close()
					}
				}
			}
			activeTurn = ""
			activeMarker = ""
		}
	}
}
