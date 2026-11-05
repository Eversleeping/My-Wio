package claudeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/wio-platform/wio/internal/protocol"
)

// ListSkills asks the native SDK initialization protocol for commands visible
// to this account and workspace. It sends no user prompt or model request.
func ListSkills(parent context.Context, executable, workspace string, environment ...string) (protocol.ClaudeSkillsResult, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	if executable == "" {
		executable = "claude"
	}
	cmd := exec.CommandContext(ctx, executable, "-p", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--no-session-persistence")
	cmd.Dir = workspace
	configureEnvironment(cmd, environment)
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return protocol.ClaudeSkillsResult{}, errors.New("could not create Claude discovery input")
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return protocol.ClaudeSkillsResult{}, errors.New("could not create Claude discovery output")
	}
	if cmd.Start() != nil {
		return protocol.ClaudeSkillsResult{}, errors.New("could not start Claude Code for skill discovery")
	}
	defer func() { _ = stdin.Close(); cancel(); _ = cmd.Wait() }()
	id := uuid.NewString()
	request := map[string]any{"type": "control_request", "request_id": id, "request": map[string]string{"subtype": "initialize"}}
	if json.NewEncoder(stdin).Encode(request) != nil {
		return protocol.ClaudeSkillsResult{}, errors.New("could not initialize Claude skill discovery")
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Subtype   string `json:"subtype"`
				Response  struct {
					Commands []struct {
						Name         string `json:"name"`
						Description  string `json:"description"`
						ArgumentHint string `json:"argumentHint"`
						Builtin      bool   `json:"builtin"`
					} `json:"commands"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return protocol.ClaudeSkillsResult{}, errors.New("invalid Claude skill discovery response")
		}
		if message.Type != "control_response" || message.Response.RequestID != id {
			continue
		}
		if message.Response.Subtype != "success" || message.Response.Response.Commands == nil {
			return protocol.ClaudeSkillsResult{}, errors.New("Claude Code could not list skills; update the CLI and retry")
		}
		result := protocol.ClaudeSkillsResult{Skills: []protocol.ClaudeSkill{}}
		seen := make(map[string]bool)
		for _, command := range message.Response.Response.Commands {
			if command.Builtin || !protocol.ValidClaudeSkillName(command.Name) || seen[command.Name] {
				continue
			}
			seen[command.Name] = true
			result.Skills = append(result.Skills, protocol.ClaudeSkill{Name: command.Name, Description: skillText(command.Description, 4096), ArgumentHint: skillText(command.ArgumentHint, 256)})
			if len(result.Skills) > 500 {
				return protocol.ClaudeSkillsResult{}, errors.New("Claude skill discovery exceeded 500 entries")
			}
		}
		return result, nil
	}
	if ctx.Err() != nil {
		return protocol.ClaudeSkillsResult{}, ctx.Err()
	}
	// Neither stderr nor CLI error payloads are safe to expose: they can carry
	// credentials or other private provider diagnostics.
	return protocol.ClaudeSkillsResult{}, errors.New("Claude Code exited without a skill discovery response")
}

func skillText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
