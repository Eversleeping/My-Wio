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
	"sync/atomic"
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
	if command.ApprovalMode == "plan" && strings.HasPrefix(strings.TrimSpace(command.Prompt), "/") {
		fields := strings.Fields(command.Prompt)
		if len(fields) == 0 || (fields[0] != "/help" && fields[0] != "/about") {
			return errors.New("this Gemini CLI command is unavailable in plan mode; switch permissions before running it")
		}
	}
	blocks := []any{}
	var goal *geminiGoalRun
	if command.Goal != nil {
		goal = &geminiGoalRun{goal: *command.Goal}
		blocks = append(blocks, map[string]string{"type": "text", "text": goal.instruction()})
	}
	if command.ApprovalMode == "plan" && !strings.HasPrefix(strings.TrimSpace(command.Prompt), "/") {
		blocks = append(blocks, map[string]string{"type": "text", "text": "In plan mode, present the complete plan in the conversation before requesting approval. Do not implement the plan without approval."})
	}
	if len(command.SteeringContext) > 0 {
		prior, _ := json.Marshal(command.SteeringContext)
		blocks = append(blocks, map[string]string{"type": "text", "text": "User updates delivered during the preceding task (retained for continuity):\n" + string(prior)})
	}
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
	bridge, closeBridge, err := a.questionServer(ctx, op, command.ThreadID, goal)
	if err != nil {
		return err
	}
	defer closeBridge()
	if command.Steering {
		var closeSteering func()
		environment, closeSteering, err = a.startSteering(ctx, op, command.ThreadID, environment)
		if err != nil {
			return fmt.Errorf("prepare Gemini steering: %w", err)
		}
		defer closeSteering()
	}

	if command.SessionID != "" {
		var cleanup func()
		environment, cleanup, err = acpHistoryEnvironment(environment)
		if err != nil {
			return fmt.Errorf("prepare Gemini history synchronization: %w", err)
		}
		defer cleanup()
	}
	cmd := exec.CommandContext(ctx, executable, "--acp", "--model", model, "--approval-mode", command.ApprovalMode)
	cmd.Dir = command.Workspace
	// Gemini 0.65's ACP converter drops AnsiOutput from PTY-backed shell
	// results. Use its native child-process mode so results remain plain text.
	// This only affects this ACP process, preserving account shell settings.
	cmd.Env = append(environment, "GEMINI_PTY_INFO=child_process")
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
	resuming := command.SessionID != ""
	sessionID := command.SessionID
	var searchHistory *nativeSearchHistory
	var planApproved atomic.Bool
	var availableCommands []acpCommand
	var sequence int
	toolCalls := 0
	var lastModelActivity time.Time
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
					if commands, ok := availableACPCommands(msg.Params); ok {
						availableCommands = commands
						if !replay {
							if err := emit("commands", map[string]any{"commands": commands}); err != nil {
								return nil, err
							}
						}
						continue
					}
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
					case "agent_thought_chunk", "usage_update":
						// Record activity without exposing the model's private thought
						// content or flooding the event stream during a long response.
						if now := time.Now(); now.Sub(lastModelActivity) >= 15*time.Second {
							lastModelActivity = now
							err = emit("activity", map[string]any{"phase": "model"})
						}
					case "agent_message_chunk":
						var c struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}
						_ = json.Unmarshal(u.Content, &c)
						if c.Type == "text" {
							if mode, ok := geminiModeUpdate(c.Text); ok && mode == "default" && planApproved.CompareAndSwap(true, false) {
								err = emit("mode", map[string]any{"approval_mode": mode})
							} else {
								err = emit("message", map[string]any{"role": "assistant", "content": c.Text, "delta": true})
							}
						}
					case "tool_call":
						// Gemini 0.65/0.66 streamHistory emits terminal tool_call
						// snapshots asynchronously, including AFTER available_commands_update
						// and interleaved with the next prompt. Live executions start
						// pending/in_progress and end with tool_call_update instead.
						if resuming && (u.Status == "completed" || u.Status == "failed") {
							continue
						}
						toolCalls++
						err = emit("tool_use", map[string]any{"tool_id": u.ToolCallID, "tool_name": u.Title, "status": u.Status, "parameters": json.RawMessage(u.Content)})
						if err == nil && (u.Status == "completed" || u.Status == "failed") {
							err = emit("tool_result", map[string]any{"tool_id": u.ToolCallID, "tool_name": u.Title, "status": u.Status, "output": json.RawMessage(u.Content)})
						}
					case "tool_call_update":
						if u.Status == "completed" || u.Status == "failed" {
							content := strings.TrimSpace(string(u.Content))
							if u.Status == "completed" && (content == "" || content == "[]" || content == "null") {
								if output, ok := searchHistory.output(ctx, u.ToolCallID); ok {
									u.Content, _ = json.Marshal([]any{map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": output}}})
								}
							}
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
							planApproval := strings.HasPrefix(p.ToolCall.ID, "exit_plan_mode_")
							planText := ""
							if planApproval {
								planApproved.Store(false)
								var err error
								planText, err = readGeminiPlan(p.ToolCall.Title, command.Workspace, sessionID, environment)
								if err != nil || emit("message", map[string]any{"role": "assistant", "content": "Proposed plan for review:\n\n" + planText, "delta": false}) != nil {
									_ = emit("message", map[string]any{"role": "assistant", "content": "The Gemini plan could not be displayed in full, so approval was cancelled. Ask Gemini to provide a readable plan and try again.", "delta": false})
									_ = write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": outcome}})
									return
								}
							}
							// ACP asks the client for confirmation even in YOLO mode.
							// Use a one-time grant so this turn cannot change saved policy.
							bridgeCall := strings.HasPrefix(p.ToolCall.ID, "mcp_"+bridge["name"].(string)+"_wio_ask_user") && strings.HasPrefix(p.ToolCall.Title, "wio_ask_user(")
							if goal != nil && strings.HasPrefix(p.ToolCall.ID, "mcp_"+bridge["name"].(string)+"_wio_goal_report") && strings.HasPrefix(p.ToolCall.Title, "wio_goal_report(") {
								bridgeCall = true
							}
							if bridgeCall || (command.ApprovalMode == "yolo" && !planApproval) {
								for _, o := range p.Options {
									if o.Kind == "allow_once" {
										outcome = map[string]string{"outcome": "selected", "optionId": o.ID}
										break
									}
								}
							} else {
								q := protocol.UserInputQuestion{ID: "permission", Header: "Gemini", Question: p.ToolCall.Title + "\n" + string(p.ToolCall.Content)}
								if planApproval {
									q.Question = "Review the complete plan shown above. Allow Gemini to leave plan mode and begin implementation?"
								}
								for _, o := range p.Options {
									q.Options = append(q.Options, struct {
										Label       string `json:"label"`
										Description string `json:"description"`
									}{o.Name, ""})
								}
								answers, err := a.ask(ctx, op, command.ThreadID, []protocol.UserInputQuestion{q})
								if err == nil {
									for _, o := range p.Options {
										if answer := answers["permission"].Answers; len(answer) > 0 && o.Name == answer[0] {
											if planApproval && (o.Kind == "allow_once" || o.Kind == "allow_always") {
												currentPlan, err := readGeminiPlan(p.ToolCall.Title, command.Workspace, sessionID, environment)
												if err != nil || currentPlan != planText {
													_ = emit("message", map[string]any{"role": "assistant", "content": "The Gemini plan changed before approval. Approval was cancelled; review the updated plan before trying again.", "delta": false})
													break
												}
												planApproved.Store(true)
											}
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
		fork, err := forkNativeHistory(ctx, protocol.GeminiForkCommand{SessionID: command.SessionID, SourceWorkspace: command.Workspace, Workspace: command.Workspace, Before: command.Rewrite}, environment)
		if err != nil {
			return err
		}
		sessionID = fork.SessionID
		resuming = sessionID != ""
	}
	params := map[string]any{"cwd": command.Workspace, "mcpServers": []any{bridge}}
	var sessionResult json.RawMessage
	if sessionID == "" {
		raw, err := call("session/new", params)
		if err != nil {
			return err
		}
		var result struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(raw, &result)
		sessionResult = raw
		sessionID = result.SessionID
	} else {
		params["sessionId"] = sessionID
		if sessionResult, err = call("session/load", params); err != nil {
			return err
		}
		var loaded struct {
			Meta struct {
				HistoryReplayed bool `json:"wioHistoryReplayed"`
			} `json:"_meta"`
		}
		if json.Unmarshal(sessionResult, &loaded) != nil || !loaded.Meta.HistoryReplayed {
			return errors.New("Gemini CLI cannot synchronize restored history; update Wio's Gemini compatibility adapter before retrying")
		}
		// The process-local loader awaits streamHistory before session/load
		// responds. Commands may follow; they no longer stand in for replay.
		if availableCommands, err = drainACPHistory(ctx, messages, readError); err != nil {
			return err
		}
	}
	if sessionID == "" {
		return errors.New("Gemini ACP did not return a session identifier")
	}
	// Do not silently show YOLO when CLI trust or administrator settings chose
	// a different mode. Official ACP returns the effective mode for new/load.
	var state struct {
		Modes *struct {
			Current string `json:"currentModeId"`
		} `json:"modes"`
	}
	if json.Unmarshal(sessionResult, &state) != nil {
		return errors.New("invalid Gemini session configuration")
	}
	if (state.Modes == nil && (command.ApprovalMode == "yolo" || command.ApprovalMode == "plan")) || (state.Modes != nil && state.Modes.Current != command.ApprovalMode) {
		return errors.New("Gemini CLI did not accept the requested approval mode; check workspace trust and administrator settings")
	}
	if err = emit("init", map[string]any{"session_id": sessionID, "model": command.Model, "thinking_level": command.ThinkingLevel, "timestamp": time.Now().UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)}); err != nil {
		return err
	}
	if availableCommands != nil {
		if err = emit("commands", map[string]any{"commands": availableCommands}); err != nil {
			return err
		}
	}
	searchHistory = newNativeSearchHistory(sessionID, command.Workspace, environment, resuming)
	replay = false
	type promptResult struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			Input  *int64 `json:"inputTokens"`
			Output *int64 `json:"outputTokens"`
		} `json:"usage"`
		Meta struct {
			Quota struct {
				Tokens struct {
					Input  *int64 `json:"input_tokens"`
					Output *int64 `json:"output_tokens"`
				} `json:"token_count"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	started := time.Now()
	var result promptResult
	var totalInput, totalOutput int64
	knownInput, knownOutput := true, true
	continuations, initialContinuations := 0, 0
	if goal != nil {
		continuations, initialContinuations = goal.goal.Continuations, goal.goal.Continuations
	}
	usageSnapshot := func() map[string]any {
		stats := map[string]any{"tool_calls": toolCalls}
		if knownInput {
			stats["input_tokens"] = totalInput
		}
		if knownOutput {
			stats["output_tokens"] = totalOutput
		}
		return stats
	}
	for {
		raw, err := call("session/prompt", map[string]any{"sessionId": sessionID, "prompt": blocks})
		if err != nil {
			return err
		}
		result = promptResult{}
		if json.Unmarshal(raw, &result) != nil {
			return errors.New("invalid Gemini prompt result")
		}
		input, output := result.Usage.Input, result.Usage.Output
		quotaInput, quotaOutput := result.Meta.Quota.Tokens.Input, result.Meta.Quota.Tokens.Output
		zeroUsage := input != nil && output != nil && *input == 0 && *output == 0
		if (input == nil || zeroUsage) && quotaInput != nil {
			input = quotaInput
		}
		if (output == nil || zeroUsage) && quotaOutput != nil {
			output = quotaOutput
		}
		if input == nil || *input < 0 {
			knownInput = false
		} else {
			totalInput += *input
		}
		if output == nil || *output < 0 {
			knownOutput = false
		} else {
			totalOutput += *output
		}
		if goal != nil {
			if err := emit("goal.usage", map[string]any{"goal_id": goal.goal.ID, "stats": usageSnapshot()}); err != nil {
				return err
			}
		}
		if goal == nil || result.StopReason != "end_turn" {
			break
		}
		report, evidence := goal.result()
		if report == "" && continuations-initialContinuations >= goal.goal.MaxContinuations {
			report = "blocked"
			evidence = "The configured continuation limit was reached before the goal was reported complete. Review progress and resume if needed."
		}
		if report != "" {
			if err := emit("goal.progress", map[string]any{"goal_id": goal.goal.ID, "continuations": continuations, "report": report, "evidence": evidence}); err != nil {
				return err
			}
			break
		}
		continuations++
		if err := emit("goal.progress", map[string]any{"goal_id": goal.goal.ID, "continuations": continuations}); err != nil {
			return err
		}
		blocks = []any{map[string]string{"type": "text", "text": goal.continuation(continuations, time.Since(started))}}
	}
	status := "success"
	if result.StopReason != "end_turn" {
		status = "error"
	}
	payload := map[string]any{"status": status, "stats": usageSnapshot(), "stop_reason": result.StopReason}
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

func geminiModeUpdate(text string) (string, bool) {
	mode, ok := strings.CutPrefix(text, "[MODE_UPDATE] ")
	if !ok {
		return "", false
	}
	switch mode {
	case "default", "auto_edit", "yolo", "plan":
		return mode, true
	default:
		return "", false
	}
}

func drainACPHistory(ctx context.Context, messages <-chan acpMessage, readError <-chan error) ([]acpCommand, error) {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("Gemini did not finish replaying session history")
		case err := <-readError:
			return nil, fmt.Errorf("Gemini history connection closed: %w", err)
		case msg := <-messages:
			if msg.Method != "session/update" {
				continue
			}
			if commands, ok := availableACPCommands(msg.Params); ok {
				return commands, nil
			}
		}
	}
}

type acpCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func availableACPCommands(raw json.RawMessage) ([]acpCommand, bool) {
	var payload struct {
		Update struct {
			Type     string       `json:"sessionUpdate"`
			Commands []acpCommand `json:"availableCommands"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Update.Type != "available_commands_update" {
		return nil, false
	}
	commands := make([]acpCommand, 0, len(payload.Update.Commands))
	seen := map[string]bool{}
	for _, command := range payload.Update.Commands {
		if len(commands) >= 50 {
			break
		}
		if len(command.Name) == 0 || len(command.Name) > 64 || strings.IndexFunc(command.Name, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 || seen[command.Name] {
			continue
		}
		seen[command.Name] = true
		if len([]rune(command.Description)) > 256 {
			command.Description = string([]rune(command.Description)[:256])
		}
		commands = append(commands, command)
	}
	return commands, true
}
