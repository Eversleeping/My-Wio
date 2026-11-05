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

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

// Run keeps prompts off the process command line and requires a complete result.
// Authentication and native transcripts remain under the Agent's account.
func Run(parent context.Context, executable, operationID string, command protocol.ClaudeTurnCommand, emit func(protocol.StreamEvent) error, environment ...string) error {
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
	input := command.Prompt
	if len(command.Images) > 0 {
		if len(command.Images) > 4 {
			return errors.New("too many Claude images")
		}
		total := 0
		blocks := []any{}
		if command.Prompt != "" {
			blocks = append(blocks, map[string]string{"type": "text", "text": command.Prompt})
		}
		for _, image := range command.Images {
			prefix, data, ok := strings.Cut(image.DataURL, ",")
			if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64" && prefix != "data:image/webp;base64") {
				return errors.New("invalid Claude image")
			}
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil || len(decoded) == 0 || len(decoded) > 2<<20 {
				return errors.New("invalid Claude image data")
			}
			total += len(decoded)
			if total > 4<<20 {
				return errors.New("Claude images are too large")
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64"), "data": data}})
		}
		raw, err := json.Marshal(map[string]any{"type": "user", "session_id": "", "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": blocks}})
		if err != nil {
			return err
		}
		input = string(raw) + "\n"
		args = append(args, "--input-format", "stream-json")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	configureEnvironment(cmd, environment)
	cmd.Dir = command.Workspace
	cmd.Stdin = strings.NewReader(input)
	configureProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Stderr can contain credentials; do not persist it in operation errors.
	if err := cmd.Start(); err != nil {
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
		return emit(protocol.StreamEvent{StreamID: command.ThreadID, Kind: "claude." + kind, Payload: raw})
	}
	for scan.Scan() {
		if len(strings.TrimSpace(scan.Text())) == 0 {
			continue
		}
		var data struct {
			Type              string          `json:"type"`
			Subtype           string          `json:"subtype"`
			SessionID         string          `json:"session_id"`
			Skills            []string        `json:"skills"`
			ParentToolUseID   *string         `json:"parent_tool_use_id"`
			IsError           bool            `json:"is_error"`
			Result            string          `json:"result"`
			Errors            []string        `json:"errors"`
			Usage             json.RawMessage `json:"usage"`
			PermissionDenials json.RawMessage `json:"permission_denials"`
			Event             struct {
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
		switch data.Type {
		case "system":
			if data.Subtype == "init" {
				if _, err := uuid.Parse(data.SessionID); err != nil || (command.SessionID != "" && command.SessionID != data.SessionID) || (command.ResumePath != "" && command.ForkSessionID == data.SessionID) {
					streamErr = errors.New("Claude Code returned an invalid or unexpected session ID")
					break
				}
				gotInit = true
				streamErr = send("init", map[string]any{"session_id": data.SessionID, "skills": data.Skills})
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
			streamErr = send("result", map[string]any{"success": success, "errors": data.Errors, "usage": data.Usage, "permission_denials": data.PermissionDenials})
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
	waitErr := cmd.Wait()
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
