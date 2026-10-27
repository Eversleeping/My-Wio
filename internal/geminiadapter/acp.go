package geminiadapter

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type acpMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (a *Adapter) runACP(parent context.Context, op string, command protocol.GeminiTurnCommand, executable string, environment []string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	environment, model, err := thinkingEnvironment(environment, command.Model, command.ThinkingLevel)
	if err != nil {
		return err
	}
	blocks := []any{}
	if command.Prompt != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": command.Prompt})
	}
	if len(command.Images) > 4 {
		return errors.New("too many Gemini images")
	}
	total := 0
	for _, image := range command.Images {
		prefix, data, ok := strings.Cut(image.DataURL, ",")
		mime := strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
		raw, e := base64.StdEncoding.DecodeString(data)
		total += len(raw)
		if !ok || prefix != "data:"+mime+";base64" || (mime != "image/png" && mime != "image/jpeg" && mime != "image/webp") || e != nil || len(raw) == 0 || len(raw) > 2<<20 || total > 4<<20 || http.DetectContentType(raw) != mime {
			return errors.New("invalid Gemini image")
		}
		blocks = append(blocks, map[string]string{"type": "image", "mimeType": mime, "data": data})
	}
	bridge, closeBridge, err := a.questionServer(ctx, op, command.ThreadID)
	if err != nil {
		return err
	}
	defer closeBridge()

	cmd := exec.CommandContext(ctx, executable, "--acp", "--model", model, "--approval-mode", command.ApprovalMode)
	cmd.Dir = command.Workspace
	cmd.Env = environment
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start Gemini CLI with ACP support: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		timer := time.AfterFunc(2*time.Second, func() { _ = cmd.Cancel() })
		_ = cmd.Wait()
		timer.Stop()
	}()
	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return json.NewEncoder(stdin).Encode(value)
	}
	messages := make(chan acpMessage, 32)
	readError := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			var msg acpMessage
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				readError <- errors.New("Gemini CLI returned invalid ACP output")
				return
			}
			select {
			case messages <- msg:
			case <-ctx.Done():
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		readError <- err
	}()
	emit := func(kind string, payload map[string]any) error {
		payload["operation_id"] = op
		raw, _ := json.Marshal(payload)
		return a.emit(protocol.StreamEvent{StreamID: command.ThreadID, Kind: "gemini." + kind, Payload: raw})
	}
	replay := true
	var sequence int
	toolCalls := 0
	// Requests are sequential; server-initiated requests are answered concurrently
	// so waiting for a user never blocks the RPC reader or cancellation.
	call := func(method string, params any) (json.RawMessage, error) {
		sequence++
		id := sequence
		if err := write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case err := <-readError:
				return nil, fmt.Errorf("Gemini ACP connection closed: %w", err)
			case msg := <-messages:
				if msg.Method == "session/update" {
					if replay {
						continue
					}
					var p struct {
						SessionID string `json:"sessionId"`
						Update    struct {
							Type       string          `json:"sessionUpdate"`
							ToolCallID string          `json:"toolCallId"`
							Title      string          `json:"title"`
							Status     string          `json:"status"`
							Content    json.RawMessage `json:"content"`
						} `json:"update"`
					}
					if json.Unmarshal(msg.Params, &p) != nil {
						return nil, errors.New("invalid Gemini update")
					}
					u := p.Update
					var err error
					switch u.Type {
					case "agent_message_chunk":
						var c struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}
						_ = json.Unmarshal(u.Content, &c)
						if c.Type == "text" {
							err = emit("message", map[string]any{"role": "assistant", "content": c.Text, "delta": true})
						}
					case "tool_call":
						toolCalls++
						err = emit("tool_use", map[string]any{"tool_id": u.ToolCallID, "tool_name": u.Title, "parameters": json.RawMessage(u.Content)})
					case "tool_call_update":
						if u.Status == "completed" || u.Status == "failed" {
							err = emit("tool_result", map[string]any{"tool_id": u.ToolCallID, "tool_name": u.Title, "status": u.Status, "output": json.RawMessage(u.Content)})
						}
					}
					if err != nil {
						return nil, err
					}
					continue
				}
				if msg.Method != "" && len(msg.ID) > 0 {
					if msg.Method == "session/request_permission" {
						go func(msg acpMessage) {
							var p struct {
								ToolCall struct {
									ID      string          `json:"toolCallId"`
									Title   string          `json:"title"`
									Content json.RawMessage `json:"content"`
								} `json:"toolCall"`
								Options []struct {
									ID   string `json:"optionId"`
									Name string `json:"name"`
									Kind string `json:"kind"`
								} `json:"options"`
							}
							_ = json.Unmarshal(msg.Params, &p)
							outcome := map[string]string{"outcome": "cancelled"}
							// Only our randomly named, question-only bridge can be auto-approved.
							bridgeCall := strings.HasPrefix(p.ToolCall.ID, "mcp_"+bridge["name"].(string)+"_wio_ask_user") && strings.HasPrefix(p.ToolCall.Title, "wio_ask_user(")
							if bridgeCall {
								for _, o := range p.Options {
									if o.Kind == "allow_once" {
										outcome = map[string]string{"outcome": "selected", "optionId": o.ID}
										break
									}
								}
							} else {
								q := protocol.UserInputQuestion{ID: "permission", Header: "Gemini", Question: p.ToolCall.Title + "\n" + string(p.ToolCall.Content)}
								for _, o := range p.Options {
									q.Options = append(q.Options, struct {
										Label       string `json:"label"`
										Description string `json:"description"`
									}{o.Name, ""})
								}
								answers, err := a.ask(ctx, op, command.ThreadID, []protocol.UserInputQuestion{q})
								if err == nil {
									for _, o := range p.Options {
										if o.Name == answers["permission"].Answers[0] {
											outcome = map[string]string{"outcome": "selected", "optionId": o.ID}
											break
										}
									}
								}
							}
							_ = write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": outcome}})
						}(msg)
					} else {
						_ = write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "unsupported client method"}})
					}
					continue
				}
				if string(msg.ID) != fmt.Sprint(id) {
					continue
				}
				if len(msg.Error) > 0 && string(msg.Error) != "null" {
					return nil, errors.New("Gemini ACP request failed (check CLI authentication, model and ACP support)")
				}
				return msg.Result, nil
			}
		}
	}
	if _, err = call("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "Wio", "version": "1"}, "clientCapabilities": map[string]any{}}); err != nil {
		return err
	}
	if command.Rewrite != nil {
		fork, err := a.Fork(ctx, op+":rewrite", protocol.GeminiForkCommand{SessionID: command.SessionID, SourceWorkspace: command.Workspace, Workspace: command.Workspace, Before: command.Rewrite})
		if err != nil {
			return err
		}
		command.SessionID = fork.SessionID
	}
	params := map[string]any{"cwd": command.Workspace, "mcpServers": []any{bridge}}
	sessionID := command.SessionID
	if sessionID == "" {
		raw, err := call("session/new", params)
		if err != nil {
			return err
		}
		var result struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(raw, &result)
		sessionID = result.SessionID
	} else {
		params["sessionId"] = sessionID
		if _, err = call("session/load", params); err != nil {
			return err
		}
		// Gemini 0.65 streams history asynchronously, even after replying to
		// session/load. Its scheduled command update follows that replay. Wait
		// for this fence before attributing any chunks to the new turn.
		if err = drainACPHistory(ctx, messages, readError); err != nil {
			return err
		}
	}
	if sessionID == "" {
		return errors.New("Gemini ACP did not return a session identifier")
	}
	if err = emit("init", map[string]any{"session_id": sessionID, "model": command.Model, "thinking_level": command.ThinkingLevel, "timestamp": time.Now().UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	replay = false
	raw, err := call("session/prompt", map[string]any{"sessionId": sessionID, "prompt": blocks})
	if err != nil {
		return err
	}
	var result struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			Input  int64 `json:"inputTokens"`
			Output int64 `json:"outputTokens"`
		} `json:"usage"`
		Meta struct {
			Quota struct {
				Tokens struct {
					Input  int64 `json:"input_tokens"`
					Output int64 `json:"output_tokens"`
				} `json:"token_count"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return errors.New("invalid Gemini prompt result")
	}
	status := "success"
	if result.StopReason != "end_turn" {
		status = "error"
	}
	input, output := result.Usage.Input, result.Usage.Output
	if input == 0 && output == 0 {
		input, output = result.Meta.Quota.Tokens.Input, result.Meta.Quota.Tokens.Output
	}
	payload := map[string]any{"status": status, "stats": map[string]any{"input_tokens": input, "output_tokens": output, "tool_calls": toolCalls}}
	if status != "success" {
		payload["error"] = map[string]string{"message": "Gemini stopped: " + result.StopReason}
	}
	if err = emit("result", payload); err != nil {
		return err
	}
	if status != "success" {
		return errors.New("Gemini turn did not finish: " + result.StopReason)
	}
	return nil
}

func drainACPHistory(ctx context.Context, messages <-chan acpMessage, readError <-chan error) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("Gemini did not finish replaying session history")
		case err := <-readError:
			return fmt.Errorf("Gemini history connection closed: %w", err)
		case msg := <-messages:
			if msg.Method != "session/update" {
				continue
			}
			var p struct {
				Update struct {
					Type string `json:"sessionUpdate"`
				} `json:"update"`
			}
			if json.Unmarshal(msg.Params, &p) == nil && p.Update.Type == "available_commands_update" {
				return nil
			}
		}
	}
}
