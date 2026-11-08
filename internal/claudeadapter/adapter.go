// Package claudeadapter runs the official Claude Code CLI in print mode.
package claudeadapter

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/childprocess"
	"github.com/wio-platform/wio/internal/protocol"
)

// Run keeps prompts off the process command line and requires a complete result.
// Authentication and native transcripts remain under the Agent's account.
func Run(parent context.Context, executable, operationID string, command protocol.ClaudeTurnCommand, emit func(protocol.StreamEvent) error, environment ...string) error {
	return run(parent, executable, operationID, command, emit, nil, environment...)
}

func run(parent context.Context, executable, operationID string, command protocol.ClaudeTurnCommand, emit func(protocol.StreamEvent) error, live *liveTurn, environment ...string) error {
	if executable == "" {
		executable = "claude"
	}
	if command.PermissionMode != "default" && command.PermissionMode != "acceptEdits" && command.PermissionMode != "plan" && command.PermissionMode != "bypassPermissions" {
		return errors.New("invalid Claude permission mode")
	}
	if command.SessionID != "" {
		if _, err := uuid.Parse(command.SessionID); err != nil {
			return errors.New("invalid Claude session ID")
		}
	}
	if !protocol.ValidClaudeEffort(command.Effort) {
		return errors.New("invalid Claude effort level")
	}
	args := []string{"-p", "--verbose", "--output-format", "stream-json", "--include-partial-messages", "--permission-mode", command.PermissionMode}
	if command.Effort != "" {
		args = append(args, "--effort", command.Effort)
	}
	if command.Model != "" {
		args = append(args, "--model", command.Model)
	}
	if command.SessionID != "" {
		args = append(args, "--resume", command.SessionID)
	} else if command.ResumePath != "" {
		args = append(args, "--resume", command.ResumePath, "--fork-session")
	}
	if command.ResumeAt != "" {
		if _, err := uuid.Parse(command.ResumeAt); err != nil || command.ResumePath == "" {
			return errors.New("invalid Claude resume boundary")
		}
		args = append(args, "--resume-session-at", command.ResumeAt)
	}
	id := operationID
	if _, err := uuid.Parse(id); err != nil {
		id = uuid.NewString()
	}
	raw, err := claudeUserInput(command.Prompt, command.Images, id, "next")
	if err != nil {
		return err
	}
	input := string(raw)
	args = append(args, "--input-format", "stream-json")
	if live != nil {
		args = append(args, "--replay-user-messages")
	}
	if command.Questions {
		if live == nil {
			return errors.New("Claude questions require a live input stream")
		}
		args = append(args, "--permission-prompt-tool", "stdio")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	configureEnvironment(cmd, environment)
	cmd.Dir = command.Workspace
	if live == nil {
		cmd.Stdin = strings.NewReader(input)
	} else {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		live.start(stdin, cancel, nil)
	}
	configureProcess(cmd)
	// Stderr can contain credentials; do not persist it in operation errors.
	stdout, err := childprocess.StartStream(cmd)
	if err != nil {
		return fmt.Errorf("start Claude Code CLI (install claude for the Agent account): %w", err)
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 64*1024), 8*1024*1024)
	gotInit, gotResult, success, partial := false, false, false, false
	var streamErr error
	send := func(kind string, data map[string]any) error {
		data["operation_id"] = operationID
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		event := protocol.StreamEvent{StreamID: command.ThreadID, Kind: "claude." + kind, Payload: raw}
		if kind == "question" {
			event.EventID, _ = data["request_id"].(string)
		}
		if kind == "question.answered" || kind == "question.cancelled" {
			id, _ := data["request_id"].(string)
			event.EventID = id + ":" + kind
		}
		if strings.HasPrefix(kind, "compaction.") {
			if id, _ := data["native_id"].(string); id != "" {
				event.EventID = "claude-compaction:" + operationID + ":" + id + ":" + kind
			}
		}
		return emit(event)
	}
	initializeID := ""
	var initializeTimer *time.Timer
	if live != nil {
		live.mu.Lock()
		live.emit = send
		live.mu.Unlock()
		initial := []byte(input)
		if command.Questions {
			initializeID = uuid.NewString()
			initial, _ = json.Marshal(map[string]any{"type": "control_request", "request_id": initializeID, "request": map[string]any{"subtype": "initialize"}})
			initial = append(initial, '\n')
			initializeTimer = time.AfterFunc(30*time.Second, cancel)
			defer initializeTimer.Stop()
		}
		if _, err := live.stdin.Write(initial); err != nil {
			cancel()
			_ = stdout.Wait()
			return errors.New("could not send initial Claude input")
		}
	}
	for scan.Scan() {
		if len(strings.TrimSpace(scan.Text())) == 0 {
			continue
		}
		if command.Questions {
			var frame struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				Response  struct {
					RequestID string `json:"request_id"`
					Subtype   string `json:"subtype"`
				} `json:"response"`
			}
			if json.Unmarshal(scan.Bytes(), &frame) == nil {
				if frame.Type == "control_response" && frame.Response.RequestID == initializeID && initializeID != "" {
					initializeTimer.Stop()
					initializeID = ""
					if frame.Response.Subtype != "success" {
						streamErr = errors.New("could not initialize Claude question support")
						break
					}
					_, streamErr = live.stdin.Write([]byte(input))
					if streamErr != nil {
						break
					}
					continue
				}
				if frame.Type == "control_request" {
					streamErr = live.questionRequest(scan.Bytes())
					if streamErr != nil {
						break
					}
					continue
				}
				if frame.Type == "control_cancel_request" {
					streamErr = live.cancelQuestion(frame.RequestID)
					if streamErr != nil {
						break
					}
					continue
				}
			}
		}
		var data struct {
			Type              string          `json:"type"`
			UUID              string          `json:"uuid"`
			IsReplay          bool            `json:"isReplay"`
			UserMessageUUID   string          `json:"user_message_uuid"`
			UserMessageUUIDs  []string        `json:"user_message_uuids"`
			QueuedTurnCount   int             `json:"queued_turn_count"`
			Subtype           string          `json:"subtype"`
			Status            string          `json:"status"`
			CompactResult     string          `json:"compact_result"`
			CompactMetadata   json.RawMessage `json:"compact_metadata"`
			SessionID         string          `json:"session_id"`
			Skills            []string        `json:"skills"`
			ParentToolUseID   *string         `json:"parent_tool_use_id"`
			IsError           bool            `json:"is_error"`
			Result            string          `json:"result"`
			Errors            []string        `json:"errors"`
			Usage             json.RawMessage `json:"usage"`
			PermissionDenials json.RawMessage `json:"permission_denials"`
			Tasks             []struct {
				Ambient bool `json:"ambient"`
			} `json:"tasks"`
			Event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scan.Bytes(), &data); err != nil {
			streamErr = errors.New("Claude Code returned invalid stream-json output")
			break
		}
		if data.ParentToolUseID != nil && !strings.HasPrefix(*data.ParentToolUseID, "forked-command-") {
			continue
		}
		if live != nil {
			if data.Type == "assistant" || data.Type == "stream_event" {
				live.progress()
			}
			ids := append(data.UserMessageUUIDs, data.UserMessageUUID)
			for _, id := range ids {
				if id != "" {
					if err := live.acknowledge(id, true); err != nil {
						streamErr = err
						break
					}
				}
			}
			if streamErr != nil {
				break
			}
			if data.Type == "user" && data.IsReplay {
				if err := live.acknowledge(data.UUID, false); err != nil {
					streamErr = err
					break
				}
			}
		}
		switch data.Type {
		case "system":
			if data.Subtype == "init" {
				if _, err := uuid.Parse(data.SessionID); err != nil || (command.SessionID != "" && command.SessionID != data.SessionID) || (command.ResumePath != "" && command.ForkSessionID == data.SessionID) {
					streamErr = errors.New("Claude Code returned an invalid or unexpected session ID")
					break
				}
				gotInit = true
				streamErr = send("init", map[string]any{"session_id": data.SessionID, "skills": data.Skills})
				if streamErr == nil && live != nil {
					streamErr = live.initialized()
				}
			} else if data.Subtype == "status" && data.Status == "compacting" {
				streamErr = send("compaction.started", map[string]any{"native_id": data.UUID})
			} else if data.Subtype == "status" && data.CompactResult == "failed" {
				// Native diagnostics can contain provider data. Only publish the state.
				streamErr = send("compaction.failed", map[string]any{"native_id": data.UUID})
			} else if data.Subtype == "compact_boundary" {
				payload := map[string]any{"native_id": data.UUID}
				var metadata struct {
					Trigger    string `json:"trigger"`
					PreTokens  *int64 `json:"pre_tokens"`
					PostTokens *int64 `json:"post_tokens"`
				}
				if json.Unmarshal(data.CompactMetadata, &metadata) == nil {
					if metadata.Trigger == "auto" || metadata.Trigger == "manual" {
						payload["trigger"] = metadata.Trigger
					}
					if metadata.PreTokens != nil && *metadata.PreTokens >= 0 {
						payload["pre_tokens"] = *metadata.PreTokens
					}
					if metadata.PostTokens != nil && *metadata.PostTokens >= 0 {
						payload["post_tokens"] = *metadata.PostTokens
					}
				}
				streamErr = send("compaction.completed", payload)
			} else if data.Subtype == "background_tasks_changed" && live != nil {
				count := 0
				for _, task := range data.Tasks {
					if !task.Ambient {
						count++
					}
				}
				streamErr = live.backgroundTasks(count)
			} else if data.Subtype == "permission_denied" {
				streamErr = send("notice", map[string]any{"text": "Claude Code denied a tool request that required approval."})
			}
		case "stream_event":
			if data.Event.Type == "content_block_delta" && data.Event.Delta.Type == "text_delta" && data.Event.Delta.Text != "" {
				partial = true
				streamErr = send("message", map[string]any{"text": data.Event.Delta.Text})
			}
		case "assistant":
			var blocks []json.RawMessage
			if json.Unmarshal(data.Message.Content, &blocks) != nil {
				streamErr = errors.New("Claude Code returned an invalid assistant message")
				break
			}
			for _, block := range blocks {
				var b struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				}
				if json.Unmarshal(block, &b) != nil {
					continue
				}
				switch b.Type {
				case "text":
					if !partial && b.Text != "" {
						streamErr = send("message", map[string]any{"text": b.Text})
					}
				case "tool_use":
					streamErr = send("tool_use", map[string]any{"id": b.ID, "name": b.Name, "input": b.Input})
				}
				if streamErr != nil {
					break
				}
			}
			partial = false
		case "user":
			var blocks []json.RawMessage
			// User messages may contain a plain string; only tool-result blocks
			// are needed here because the user prompt is saved before dispatch.
			_ = json.Unmarshal(data.Message.Content, &blocks)
			for _, block := range blocks {
				var b struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
					IsError   bool            `json:"is_error"`
				}
				if json.Unmarshal(block, &b) == nil && b.Type == "tool_result" {
					streamErr = send("tool_result", map[string]any{"id": b.ToolUseID, "content": b.Content, "is_error": b.IsError})
					if streamErr != nil {
						break
					}
				}
			}
		case "result":
			gotResult = true
			success = !data.IsError && data.Subtype == "success"
			streamErr = send("result", map[string]any{"result_id": data.UUID, "success": success, "errors": data.Errors, "usage": data.Usage, "permission_denials": data.PermissionDenials})
			if streamErr == nil && live != nil {
				streamErr = live.result(data.QueuedTurnCount)
			}
		}
		if streamErr != nil {
			break
		}
	}
	if streamErr == nil {
		streamErr = scan.Err()
	}
	if streamErr != nil {
		cancel()
	}
	waitErr := stdout.Wait()
	if streamErr != nil {
		return streamErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("Claude Code failed; check authentication, model access and tool results: %w", waitErr)
	}
	if !gotInit || !gotResult {
		return errors.New("Claude Code ended without a complete session result")
	}
	if !success {
		return errors.New("Claude Code turn failed; see the session result")
	}
	return nil
}

func configureEnvironment(cmd *exec.Cmd, environment []string) {
	if len(environment) != 0 {
		// Remove inherited values before adding managed credentials and Git
		// settings so only the selected server profile reaches this process.
		overridden := make(map[string]bool, len(environment))
		for _, entry := range environment {
			key, _, _ := strings.Cut(entry, "=")
			overridden[strings.ToUpper(key)] = true
		}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !overridden[strings.ToUpper(key)] {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, environment...)
	}
}

func claudeUserInput(prompt string, images []protocol.TurnImage, id, priority string) ([]byte, error) {
	if len(images) > 4 {
		return nil, errors.New("too many Claude images")
	}
	blocks := []any{}
	if prompt != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": prompt})
	}
	total := 0
	for _, image := range images {
		prefix, data, ok := strings.Cut(image.DataURL, ",")
		if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64" && prefix != "data:image/webp;base64") {
			return nil, errors.New("invalid Claude image")
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(decoded) == 0 || len(decoded) > 2<<20 {
			return nil, errors.New("invalid Claude image data")
		}
		total += len(decoded)
		if total > 4<<20 {
			return nil, errors.New("Claude images are too large")
		}
		blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64"), "data": data}})
	}
	raw, err := json.Marshal(map[string]any{"type": "user", "uuid": id, "session_id": "", "parent_tool_use_id": nil, "priority": priority, "origin": map[string]string{"kind": "human"}, "message": map[string]any{"role": "user", "content": blocks}})
	return append(raw, '\n'), err
}
