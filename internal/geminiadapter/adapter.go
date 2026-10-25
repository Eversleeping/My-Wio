// Package geminiadapter integrates the official Gemini CLI stream-json protocol.
package geminiadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wio-platform/wio/internal/protocol"
)

type Adapter struct {
	command      string
	emit         func(protocol.StreamEvent) error
	mu           sync.Mutex
	active       map[string]context.CancelFunc
	cancelled    map[string]time.Time
	updating     bool
	environment  []string
	defaultModel string
}

// Apply persistent API settings only between turns. The reservation also
// excludes concurrent executable changes without blocking inventory probes.
func (a *Adapter) ReconfigureEnvironment(environment []string, model string, apply func() error) error {
	a.mu.Lock()
	if a.updating || len(a.active) != 0 {
		a.mu.Unlock()
		return errors.New("finish or interrupt active Gemini turns before changing API settings")
	}
	a.updating = true
	a.mu.Unlock()
	err := apply()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updating = false
	if err == nil {
		a.environment = append([]string(nil), environment...)
		a.defaultModel = model
	}
	return err
}

func mergedEnvironment(overrides []string) []string {
	env := os.Environ()
	for _, override := range overrides {
		key, _, _ := strings.Cut(override, "=")
		kept := env[:0]
		for _, entry := range env {
			name, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(name, key) {
				kept = append(kept, entry)
			}
		}
		env = append(kept, override)
	}
	return env
}

func (a *Adapter) Command() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.command
}

// Reserve the adapter while installing without blocking heartbeats. A failed
// install preserves the command and existing native sessions.
func (a *Adapter) ReconfigureCommand(command string, install func() error) error {
	a.mu.Lock()
	if a.updating || len(a.active) != 0 {
		a.mu.Unlock()
		return errors.New("finish or interrupt active Gemini turns before changing versions")
	}
	a.updating = true
	a.mu.Unlock()
	err := install()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updating = false
	if err == nil {
		a.command = command
	}
	return err
}

func New(command string, emit func(protocol.StreamEvent) error) *Adapter {
	if command == "" {
		command = "gemini"
	}
	return &Adapter{command: command, emit: emit, active: map[string]context.CancelFunc{}, cancelled: map[string]time.Time{}}
}

func (a *Adapter) Interrupt(operationID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cancel := a.active[operationID]; cancel != nil {
		cancel()
	} else {
		a.cancelled[operationID] = time.Now()
	}
}

func (a *Adapter) Run(parent context.Context, operationID string, command protocol.GeminiTurnCommand) error {
	return a.run(parent, operationID, command, false)
}

// RunInTrustedWorkspace is for a user-selected workspace whose canonical path
// the Agent has already checked against its configured roots. Trust applies only
// to this CLI process; it does not change the user's persistent trust decisions
// or the requested tool approval mode.
func (a *Adapter) RunInTrustedWorkspace(parent context.Context, operationID string, command protocol.GeminiTurnCommand) error {
	return a.run(parent, operationID, command, true)
}

func (a *Adapter) run(parent context.Context, operationID string, command protocol.GeminiTurnCommand, trustedWorkspace bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return errors.New("Gemini configuration is changing; try again after it finishes")
	}
	executable := a.command
	overrides := append([]string(nil), a.environment...)
	if trustedWorkspace {
		overrides = append(overrides, "GEMINI_CLI_TRUST_WORKSPACE=true")
	}
	environment := mergedEnvironment(overrides)
	if (command.Model == "" || command.Model == "auto") && a.defaultModel != "" {
		command.Model = a.defaultModel
	}
	_, cancelled := a.cancelled[operationID]
	delete(a.cancelled, operationID)
	for id, at := range a.cancelled {
		if time.Since(at) > 15*time.Minute {
			delete(a.cancelled, id)
		}
	}
	if cancelled {
		a.mu.Unlock()
		return context.Canceled
	}
	if _, exists := a.active[operationID]; exists {
		a.mu.Unlock()
		return errors.New("Gemini turn is already running")
	}
	a.active[operationID] = cancel
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.active, operationID); a.mu.Unlock() }()
	if command.ApprovalMode != "default" && command.ApprovalMode != "auto_edit" {
		return errors.New("invalid Gemini approval mode")
	}
	// Keep prompts out of the process argument list. Piped input is documented
	// headless mode and is handled as the complete prompt by Gemini CLI.
	args := []string{"--output-format", "stream-json", "--model", command.Model, "--approval-mode", command.ApprovalMode}
	if command.Rewrite != nil {
		path, err := prepareRewrite(command, environment)
		if err != nil {
			return err
		}
		defer os.Remove(path)
		args = append(args, "--session-file", path)
	} else if command.SessionID != "" {
		args = append(args, "--resume", command.SessionID)
	}
	environment, cliModel, err := thinkingEnvironment(environment, command.Model, command.ThinkingLevel)
	if err != nil {
		return err
	}
	args[3] = cliModel
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = command.Workspace
	cmd.Env = environment
	cmd.Stdin = strings.NewReader(command.Prompt)
	configureProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr may include credentials or provider diagnostics. The structured
	// stream carries user-facing errors; never persist raw stderr in operations.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Gemini CLI (install @google/gemini-cli for the Agent account): %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	gotResult, success := false, false
	var streamErr error
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(line, &payload); err != nil {
			streamErr = errors.New("Gemini CLI returned invalid stream-json output")
			break
		}
		kind, _ := payload["type"].(string)
		switch kind {
		case "init", "message", "tool_use", "tool_result", "error", "result":
		default:
			continue
		}
		// The local user event is saved transactionally before dispatch.
		if kind == "message" && payload["role"] == "user" {
			continue
		}
		if kind == "init" && command.Rewrite != nil {
			sessionID, _ := payload["session_id"].(string)
			if sessionID == "" || sessionID == command.SessionID {
				streamErr = errors.New("Gemini did not create a replacement context; original conversation was kept")
				break
			}
		}
		payload["operation_id"] = operationID
		if kind == "init" {
			payload["thinking_level"] = command.ThinkingLevel
			if command.ThinkingLevel != "" {
				payload["model"] = command.Model
			}
		}
		raw, _ := json.Marshal(payload)
		if err := a.emit(protocol.StreamEvent{StreamID: command.ThreadID, Kind: "gemini." + kind, Payload: raw}); err != nil {
			streamErr = err
			break
		}
		if kind == "result" {
			gotResult = true
			success = payload["status"] == "success"
		}
	}
	if streamErr == nil {
		streamErr = scanner.Err()
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
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) && exitErr.ExitCode() == 55 {
			return fmt.Errorf("Gemini CLI rejected an untrusted workspace; update the Wio Agent or review Gemini workspace trust settings: %w", waitErr)
		}
		return fmt.Errorf("Gemini CLI failed; check Gemini authentication, model access and tool results: %w", waitErr)
	}
	if !gotResult {
		return errors.New("Gemini CLI ended without a result event; check CLI version and authentication")
	}
	if !success {
		return errors.New("Gemini turn failed; see the structured error or tool result")
	}
	return nil
}
